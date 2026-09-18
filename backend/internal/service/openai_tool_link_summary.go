package service

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const opsOpenAIInputToolSummaryKey = "ops_openai_input_tool_summary"

// Counts only. IDs, tool names, arguments, output and encrypted state must not
// enter this diagnostic. Unmatched means absent from this payload, not invalid:
// previous_response_id, item_reference or compaction can supply other context.
type OpenAIToolLinkSummary struct {
	InputTypes             map[string]int `json:"input_types"`
	Calls                  int            `json:"calls"`
	Outputs                int            `json:"outputs"`
	PairedOutputs          int            `json:"paired_outputs"`
	UnmatchedOutputs       int            `json:"unmatched_outputs"`
	MissingCallIDs         int            `json:"missing_call_ids"`
	DuplicateCallIDs       int            `json:"duplicate_call_ids"`
	DuplicateOutputCallIDs int            `json:"duplicate_output_call_ids"`
	AdditionalTools        int            `json:"additional_tools"`
	HasPreviousResponse    bool           `json:"has_previous_response_id"`
}

func setOpsOpenAIInputToolSummary(c *gin.Context, body []byte) {
	if c != nil {
		c.Set(opsOpenAIInputToolSummaryKey, summarizeOpenAIToolLinks(body))
	}
}

func summarizeOpenAIToolLinks(body []byte) *OpenAIToolLinkSummary {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return nil
	}
	s := &OpenAIToolLinkSummary{InputTypes: make(map[string]int), HasPreviousResponse: openAIJSONFieldHasValue(body, "previous_response_id")}
	calls, outputs := make(map[string]int), make(map[string]int)
	input.ForEach(func(_, item gjson.Result) bool {
		typ := strings.TrimSpace(item.Get("type").String())
		label := typ
		call, output := isCodexToolCallContextItemType(typ), isCodexToolCallOutputItemType(typ)
		if !call && !output {
			switch typ {
			case "", "message":
				label = "message"
			case "reasoning", "item_reference", "compaction", "compaction_summary", "additional_tools":
			default:
				label = "other"
			}
		}
		s.InputTypes[label]++
		if typ == "additional_tools" {
			item.Get("tools").ForEach(func(_, _ gjson.Result) bool { s.AdditionalTools++; return true })
		}
		if !call && !output {
			return true
		}
		id := strings.TrimSpace(item.Get("call_id").String())
		if call {
			s.Calls++
		} else {
			s.Outputs++
		}
		if id == "" {
			s.MissingCallIDs++
			return true
		}
		if call {
			calls[id]++
			if calls[id] > 1 {
				s.DuplicateCallIDs++
			}
		} else {
			outputs[id]++
			if outputs[id] > 1 {
				s.DuplicateOutputCallIDs++
			}
		}
		return true
	})
	for id, n := range outputs {
		if calls[id] > 0 {
			s.PairedOutputs += n
		} else {
			s.UnmatchedOutputs += n
		}
	}
	return s
}
