package agents

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/chenhongyang/novel-studio/internal/domain"
	"github.com/voocel/agentcore"
)

// P5-02 长篇窗口回归：deepseek-v4-flash 常把 plan grounding 裁决写成文本
// 而非 submit_plan_grounding_verdict 工具调用（19 连败实测）。回退解析
// 必须与工具调用路径同验证强度：唯一 {pass, findings} 平衡块、严格解码。
func TestSalvagePlanGroundingVerdictJSON(t *testing.T) {
	verdictJSON := `{"pass": false, "findings": [{"kind": "time", "plan_path": "/chapters/0/core_event", "plan_quote": "清晨点交", "source_path": "/activation/cycles/0", "source_quote": "交接在夜间完成", "explanation": "时序矛盾"}]}`
	textBlock := func(text string) agentcore.ContentBlock {
		return agentcore.ContentBlock{Type: agentcore.ContentText, Text: text}
	}
	strictDecode := func(raw []byte) (*struct {
		Pass     *bool                          `json:"pass"`
		Findings *[]domain.PlanGroundingFinding `json:"findings"`
	}, error) {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		var wire struct {
			Pass     *bool                          `json:"pass"`
			Findings *[]domain.PlanGroundingFinding `json:"findings"`
		}
		err := decoder.Decode(&wire)
		return &wire, err
	}

	raw, ok := salvagePlanGroundingVerdictJSON([]agentcore.ContentBlock{
		textBlock("裁决如下：\n" + verdictJSON + "\n以上。"),
	})
	if !ok {
		t.Fatal("文本裁决未被提取")
	}
	wire, err := strictDecode(raw)
	if err != nil {
		t.Fatalf("提取块无法严格解码: %v", err)
	}
	if wire.Pass == nil || *wire.Pass {
		t.Fatalf("pass 应为 false: %+v", wire.Pass)
	}
	if len(*wire.Findings) != 1 || (*wire.Findings)[0].Kind != "time" {
		t.Fatalf("findings 解析错误: %+v", wire.Findings)
	}

	// 无 JSON 的纯文本 → 拒绝。
	if _, ok := salvagePlanGroundingVerdictJSON([]agentcore.ContentBlock{textBlock("我认为计划可行，没有矛盾。")}); ok {
		t.Fatal("纯文本不应被提取")
	}
	// 含 { 但无 pass/findings 的对象 → 拒绝。
	if _, ok := salvagePlanGroundingVerdictJSON([]agentcore.ContentBlock{textBlock(`背景 {"chapter": 1, "title": "开篇"} 继续`)}); ok {
		t.Fatal("非裁决对象不应被提取")
	}
	// 多块拼接提取。
	nested := `{"pass": true, "findings": []}`
	raw, ok = salvagePlanGroundingVerdictJSON([]agentcore.ContentBlock{
		textBlock("前言 "),
		textBlock(nested),
		textBlock(" 后记"),
	})
	if !ok || string(raw) != nested {
		t.Fatalf("多块提取失败: ok=%v raw=%s", ok, string(raw))
	}
	// 围栏包裹。
	fenced := "```json\n" + nested + "\n```"
	if raw, ok = salvagePlanGroundingVerdictJSON([]agentcore.ContentBlock{textBlock(fenced)}); !ok || string(raw) != nested {
		t.Fatalf("围栏提取失败: ok=%v raw=%s", ok, string(raw))
	}
}
