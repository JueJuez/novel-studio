package shortstory

import (
	"context"
	"fmt"
	"strings"

	"github.com/chenhongyang/novel-studio/internal/bootstrap"
	"github.com/chenhongyang/novel-studio/internal/models"
	"github.com/voocel/agentcore"
)

// ModelRuntime 把短篇管线接到既有模型栈（计划 P1-04）：配置走
// bootstrap.LoadConfig + NewModelSet，路由沿用 ModelSet 的 role/failover
// 机制，客户端是 agentcore.ChatModel——本包不建立任何新的 provider 客户端。
// 真实云端连通验证单列在 P5-02 与最终验收，本文件不发起真实调用。
type ModelRuntime struct {
	models *bootstrap.ModelSet
	// thinking 是配置解析出的角色推理强度（角色级 → 顶层默认 → 空）。
	// 长篇经 agents 层应用同一配置（build.go roleThinking）；短篇直连
	// ChatModel，必须在本包等价应用，否则 off/low 被静默忽略——P5-02
	// 实测 deepseek-v4-flash 推理耗尽全部输出上限，facts 响应为空。
	thinking agentcore.ThinkingLevel
}

// shortStoryThinkingLevels 与长篇侧 agents.ParseThinkingLevel 接受的值
// 保持一致；非法值视为未配置（不覆盖模型默认）。
var shortStoryThinkingLevels = map[string]bool{
	"off": true, "low": true, "medium": true, "high": true,
	"xhigh": true, "max": true, "ultra": true,
}

// LoadModelRuntime 用现有配置体系加载模型路由集合。
func LoadModelRuntime(configPath string) (*ModelRuntime, error) {
	cfg, err := bootstrap.LoadConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("shortstory model: 加载配置失败: %w", err)
	}
	set, err := bootstrap.NewModelSet(cfg)
	if err != nil {
		return nil, fmt.Errorf("shortstory model: 构建模型集合失败: %w", err)
	}
	runtime := &ModelRuntime{models: set}
	if level := cfg.ResolveReasoningEffort("writer"); shortStoryThinkingLevels[level] {
		runtime.thinking = agentcore.ThinkingLevel(level)
	}
	return runtime, nil
}

// ForRole 按现有路由语义取模型（role 即 ModelSet 的 purpose/role 标识，
// 例如 "shortstory-draft"）。配置了推理强度时返回附加该选项的装饰模型。
func (r *ModelRuntime) ForRole(role string) agentcore.ChatModel {
	if r == nil || r.models == nil {
		return nil
	}
	inner := r.models.ForDefaultPurpose(role)
	if r.thinking == "" {
		return inner
	}
	return thinkingModel{inner: inner, thinking: r.thinking}
}

// thinkingModel 给底层模型的所有调用统一附加 WithThinking 选项。
type thinkingModel struct {
	inner    agentcore.ChatModel
	thinking agentcore.ThinkingLevel
}

func (m thinkingModel) Generate(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	return m.inner.Generate(ctx, messages, tools, m.withThinking(opts)...)
}

func (m thinkingModel) GenerateStream(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	return m.inner.GenerateStream(ctx, messages, tools, m.withThinking(opts)...)
}

func (m thinkingModel) SupportsTools() bool {
	return m.inner.SupportsTools()
}

func (m thinkingModel) withThinking(opts []agentcore.CallOption) []agentcore.CallOption {
	return append(append([]agentcore.CallOption(nil), opts...), agentcore.WithThinking(m.thinking))
}

// StageRequest 是一次短篇生成调用的输入。
type StageRequest struct {
	Purpose      string // 路由/记账标识，如 "shortstory-draft"
	SystemPrompt string
	UserPrompt   string
	MaxTokens    int
}

// StageResult 携带正文与本次调用的用量/成本。费用来源沿用现有
// reported/estimated/unknown 口径：优先 provider 上报价，缺价显示未知而非零。
type StageResult struct {
	Text     string
	Usage    models.TokenUsage
	Cost     models.TokenCost
	Provider string
	Model    string
}

// Generate 非流式调用一次模型。ctx 取消直接传播到 provider 客户端。
func Generate(ctx context.Context, model agentcore.ChatModel, req StageRequest) (*StageResult, error) {
	messages, opts, err := stageCallInputs(req)
	if err != nil {
		return nil, err
	}
	resp, err := model.Generate(ctx, messages, nil, opts...)
	if err != nil {
		return nil, fmt.Errorf("shortstory model %s: %w", req.Purpose, err)
	}
	return resultFromMessage(resp.Message)
}

// GenerateStream 流式调用：每个正文增量回调 onDelta；ctx 取消中断循环
// 并传播；最终用量取自 done 事件携带的完整消息。
func GenerateStream(ctx context.Context, model agentcore.ChatModel, req StageRequest, onDelta func(string)) (*StageResult, error) {
	messages, opts, err := stageCallInputs(req)
	if err != nil {
		return nil, err
	}
	events, err := model.GenerateStream(ctx, messages, nil, opts...)
	if err != nil {
		return nil, fmt.Errorf("shortstory model %s: %w", req.Purpose, err)
	}
	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("shortstory model %s: %w", req.Purpose, ctx.Err())
		case event, ok := <-events:
			if !ok {
				return nil, fmt.Errorf("shortstory model %s: 流式通道在 done 之前关闭", req.Purpose)
			}
			if event.Err != nil {
				return nil, fmt.Errorf("shortstory model %s: %w", req.Purpose, event.Err)
			}
			switch event.Type {
			case agentcore.StreamEventTextDelta:
				if onDelta != nil {
					onDelta(event.Delta)
				}
			case agentcore.StreamEventDone:
				return resultFromMessage(event.Message)
			}
		}
	}
}

func stageCallInputs(req StageRequest) ([]agentcore.Message, []agentcore.CallOption, error) {
	if strings.TrimSpace(req.SystemPrompt) == "" || strings.TrimSpace(req.UserPrompt) == "" {
		return nil, nil, fmt.Errorf("shortstory model %s: system/user prompt 不能为空", req.Purpose)
	}
	messages := []agentcore.Message{
		agentcore.SystemMsg(req.SystemPrompt),
		agentcore.UserMsg(req.UserPrompt),
	}
	var opts []agentcore.CallOption
	if req.MaxTokens > 0 {
		opts = append(opts, agentcore.WithMaxTokens(req.MaxTokens))
	}
	return messages, opts, nil
}

// resultFromMessage 从最终消息提取正文与用量/成本。usage 映射沿用
// models.TokenUsage 的 input/output/cache_read/cache_write 字段口径。
func resultFromMessage(message agentcore.Message) (*StageResult, error) {
	var text strings.Builder
	for _, block := range message.Content {
		if block.Type == agentcore.ContentText {
			text.WriteString(block.Text)
		}
	}
	result := &StageResult{Text: text.String()}
	if message.Usage == nil {
		return result, nil
	}
	result.Usage = models.TokenUsage{
		Input:      message.Usage.Input,
		Output:     message.Usage.Output,
		CacheRead:  message.Usage.CacheRead,
		CacheWrite: message.Usage.CacheWrite,
	}
	result.Provider = message.Usage.Provider
	result.Model = message.Usage.Model
	var reported *float64
	if message.Usage.Cost != nil {
		total := message.Usage.Cost.Total
		reported = &total
	}
	var entry *models.ModelEntry
	if result.Model != "" {
		if resolved, ok := models.DefaultRegistry().Resolve(result.Model); ok {
			entry = resolved
		}
	}
	result.Cost = models.ResolveTokenCost(result.Usage, entry, reported)
	return result, nil
}
