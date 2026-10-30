package shortstory

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/chenhongyang/novel-studio/internal/models"
	"github.com/voocel/agentcore"
)

// fakeChatModel 只实现 agentcore.ChatModel 的最小契约，用于在不发起
// 真实调用的前提下验证路由入参、取消传播与用量/成本解析。
type fakeChatModel struct {
	mu               sync.Mutex
	generateSum      int
	lastMessages     []agentcore.Message
	response         agentcore.Message
	genErr           error
	blockUntilCancel bool // 模拟 provider 只在 ctx 取消后返回
	streamEvents     []agentcore.StreamEvent
	streamErr        error
	neverEmit        bool
}

func (f *fakeChatModel) Generate(ctx context.Context, messages []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	f.mu.Lock()
	f.generateSum++
	f.lastMessages = messages
	response, genErr, block := f.response, f.genErr, f.blockUntilCancel
	f.mu.Unlock()
	if block {
		<-ctx.Done() // 模拟真实 provider：ctx 取消必须传播到调用
		return nil, ctx.Err()
	}
	if genErr != nil {
		return nil, genErr
	}
	return &agentcore.LLMResponse{Message: response}, nil
}

func (f *fakeChatModel) GenerateStream(_ context.Context, _ []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	if f.streamErr != nil {
		return nil, f.streamErr
	}
	events := make(chan agentcore.StreamEvent, len(f.streamEvents)+1)
	if f.neverEmit {
		return events, nil
	}
	for _, event := range f.streamEvents {
		events <- event
	}
	close(events)
	return events, nil
}

func (f *fakeChatModel) SupportsTools() bool { return false }

func stageSampleRequest() StageRequest {
	return StageRequest{
		Purpose:      "shortstory-draft",
		SystemPrompt: "你是短篇场景写手。",
		UserPrompt:   "写出第一个场景。",
		MaxTokens:    512,
	}
}

func TestShortStoryGenerateExtractsTextUsageAndCost(t *testing.T) {
	fake := &fakeChatModel{response: agentcore.Message{
		Content: []agentcore.ContentBlock{
			{Type: agentcore.ContentText, Text: "深夜的路口。"},
			{Type: agentcore.ContentText, Text: "老周点了火。"},
		},
		Usage: &agentcore.Usage{
			Provider: "deepseek", Model: "probe-model",
			Input: 100, Output: 50, CacheRead: 10, CacheWrite: 5,
			Cost: &agentcore.Cost{Total: 0.0123},
		},
	}}
	result, err := Generate(context.Background(), fake, stageSampleRequest())
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "深夜的路口。老周点了火。" {
		t.Fatalf("正文提取错误: %q", result.Text)
	}
	if result.Usage != (models.TokenUsage{Input: 100, Output: 50, CacheRead: 10, CacheWrite: 5}) {
		t.Fatalf("用量映射错误: %+v", result.Usage)
	}
	if result.Cost.Source != models.CostSourceReported || result.Cost.USD < 0.0122 || result.Cost.USD > 0.0124 {
		t.Fatalf("成本口径错误: %+v", result.Cost)
	}
	if result.Provider != "deepseek" || result.Model != "probe-model" {
		t.Fatalf("provider/model 未回填: %+v", result)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.lastMessages) != 2 || fake.lastMessages[0].Role != "system" || fake.lastMessages[1].Role != "user" {
		t.Fatalf("调用消息结构错误: %+v", fake.lastMessages)
	}
	if !strings.Contains(fake.lastMessages[1].Content[0].Text, "第一个场景") {
		t.Fatalf("user prompt 未透传: %+v", fake.lastMessages[1])
	}
}

func TestShortStoryGenerateUnknownCostStaysUnknown(t *testing.T) {
	fake := &fakeChatModel{response: agentcore.Message{
		Content: []agentcore.ContentBlock{{Type: agentcore.ContentText, Text: "正文"}},
		Usage:   &agentcore.Usage{Model: "totally-unknown-model-xyz", Input: 10, Output: 5},
	}}
	result, err := Generate(context.Background(), fake, stageSampleRequest())
	if err != nil {
		t.Fatal(err)
	}
	if result.Cost.Source != models.CostSourceUnknown {
		t.Fatalf("未知价格不得显示为零: %+v", result.Cost)
	}
}

func TestShortStoryGenerateRejectsEmptyPrompts(t *testing.T) {
	fake := &fakeChatModel{}
	if _, err := Generate(context.Background(), fake, StageRequest{Purpose: "x"}); err == nil {
		t.Fatal("空 prompt 未被拒绝")
	}
}

func TestShortStoryGeneratePropagatesCancellation(t *testing.T) {
	fake := &fakeChatModel{blockUntilCancel: true}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 已取消的 ctx：阻塞型 fake 立即返回 ctx.Err
	_, err := Generate(ctx, fake, stageSampleRequest())
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("取消未传播: %v", err)
	}
}

func TestShortStoryGenerateStreamCollectsDeltasAndFinal(t *testing.T) {
	finalMessage := agentcore.Message{
		Content: []agentcore.ContentBlock{{Type: agentcore.ContentText, Text: "深夜的路口。老周点了火。"}},
		Usage:   &agentcore.Usage{Provider: "p", Model: "m", Input: 7, Output: 3},
	}
	fake := &fakeChatModel{streamEvents: []agentcore.StreamEvent{
		{Type: agentcore.StreamEventTextDelta, Delta: "深夜的路口。"},
		{Type: agentcore.StreamEventTextDelta, Delta: "老周点了火。"},
		{Type: agentcore.StreamEventDone, Message: finalMessage},
	}}
	var deltas []string
	result, err := GenerateStream(context.Background(), fake, stageSampleRequest(), func(delta string) {
		deltas = append(deltas, delta)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 2 || deltas[0] != "深夜的路口。" || deltas[1] != "老周点了火。" {
		t.Fatalf("增量顺序错误: %v", deltas)
	}
	if result.Text != finalMessage.Content[0].Text {
		t.Fatalf("最终正文不一致: %q", result.Text)
	}
	if result.Usage.Output != 3 {
		t.Fatalf("流式用量缺失: %+v", result.Usage)
	}
}

func TestShortStoryGenerateStreamCancelAndError(t *testing.T) {
	// 流中错误事件。
	fake := &fakeChatModel{streamEvents: []agentcore.StreamEvent{
		{Type: agentcore.StreamEventTextDelta, Delta: "partial"},
		{Type: agentcore.StreamEventDone, Err: errors.New("provider 限流")},
	}}
	if _, err := GenerateStream(context.Background(), fake, stageSampleRequest(), nil); err == nil || !strings.Contains(err.Error(), "provider 限流") {
		t.Fatalf("流中错误未传播: %v", err)
	}
	// ctx 取消：通道永不产出。
	fakeCancel := &fakeChatModel{neverEmit: true}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := GenerateStream(ctx, fakeCancel, stageSampleRequest(), nil); err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("流式取消未传播: %v", err)
	}
}

func TestShortStoryModelRuntimeNilSafetyAndConfigWiring(t *testing.T) {
	var runtime *ModelRuntime
	if model := runtime.ForRole("shortstory-draft"); model != nil {
		t.Fatal("空 runtime 不应返回模型")
	}
	// 配置接入走 bootstrap.LoadConfig：坏路径必须显式报错，不静默回退。
	if _, err := LoadModelRuntime(filepath.Join(t.TempDir(), "missing-config.json")); err == nil {
		t.Fatal("缺失配置未被拒绝")
	}
}
