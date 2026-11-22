package workorder

import (
	"encoding/json"
	"fmt"
	"strings"
)

// MarshalReport encodes the report deterministically.
func MarshalReport(report *Report) ([]byte, error) {
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

// RenderMarkdown produces the human deliverable: replay batches in original
// commit order plus the groups that cannot be recovered unambiguously. It is
// derived only from the report, so Markdown and JSON cannot drift.
func RenderMarkdown(report *Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# 重放整理：%s\n\n", report.Order)
	if report.Description != "" {
		fmt.Fprintf(&b, "%s\n\n", report.Description)
	}
	fmt.Fprintf(&b, "- 基线形状摘要：`%s`\n", report.BaselineDigest)
	if report.FinalDigest != "" {
		fmt.Fprintf(&b, "- 全量重放后摘要：`%s`\n", report.FinalDigest)
	}
	fmt.Fprintf(&b, "- 提交顺序：%s\n", inlineList(report.CommitOrder))
	fmt.Fprintf(&b, "- 事务组：%d（唯一 %d，多解 %d，无法恢复 %d，受阻 %d）\n",
		len(report.Groups), countClass(report.Groups, "unique"), len(report.Ambiguous),
		len(report.Unrecoverable), len(report.Blocked))
	fmt.Fprintf(&b, "- 任何排序都只能整组发送：%s\n\n", inlineList(report.WholeGroupOnly))

	b.WriteString("## 重放批次\n\n")
	b.WriteString("每组按提交顺序整组发送一次 `POST /edits`；`edits` 的下标与 `source_lines` 一一对应。\n\n")
	for index, group := range report.Groups {
		fmt.Fprintf(&b, "### %d. `%s` — %s\n\n", index+1, group.Name, classLabel(group.Classification))
		fmt.Fprintf(&b, "- 涉及图层：%s\n", inlineList(group.Layers))
		fmt.Fprintf(&b, "- 源行（接收顺序）：%s\n", joinInts(lineNumbers(group.Rows)))
		if group.Classification == "unique" {
			fmt.Fprintf(&b, "- 重放源行顺序：`%s`\n", joinInts(group.Replay.SourceLines))
			fmt.Fprintf(&b, "- 最终形状摘要：`%s`\n\n", group.FinalDigest)
			b.WriteString("```json\n")
			raw, _ := json.Marshal(group.Replay.Request)
			b.Write(raw)
			b.WriteString("\n```\n\n")
		} else {
			b.WriteString("\n")
		}
		switch group.Classification {
		case "ambiguous":
			fmt.Fprintf(&b, "存在 %d 种合法最终形状，账本未记录原提交是哪一种：\n\n", len(group.Shapes))
			for _, shape := range group.Shapes {
				fmt.Fprintf(&b, "- `%s`（%d 种排序），例如源行 `%s`\n", shape.Digest, shape.Count, joinInts(shape.Examples[0]))
			}
			b.WriteString("\n不可无歧义恢复；需人工指定目标形状后才能生成批次。\n\n")
		case "unrecoverable":
			b.WriteString("没有任何排序能对基线成立，无法恢复。\n\n")
		case "blocked":
			b.WriteString("前序多解组未定，本组无法判定。\n\n")
		case "unresolved":
			b.WriteString("组内行数超出穷举上限，未判定。\n\n")
		}
		if group.ReplayNeedsWholeGroup {
			b.WriteString("按本批次顺序必须整组一次发送；拆批会在下列边界被拒绝：\n\n")
			for _, failure := range group.SplitFailures {
				fmt.Fprintf(&b, "- 源行 %d 之后切分：前缀批次被拒绝 `%s` — %s\n",
					failure.AfterLine, failure.Code, failure.Message)
			}
			b.WriteString("\n")
		}
		for _, note := range group.Notes {
			fmt.Fprintf(&b, "> %s\n", note)
		}
		if len(group.Notes) > 0 {
			b.WriteString("\n")
		}
	}

	b.WriteString("## 无法无歧义恢复的清单\n\n")
	if len(report.Ambiguous) == 0 && len(report.Unrecoverable) == 0 &&
		len(report.Blocked) == 0 && !hasUnresolved(report.Groups) {
		b.WriteString("无。\n")
		return b.String()
	}
	for _, group := range report.Groups {
		switch {
		case group.Classification == "ambiguous":
			fmt.Fprintf(&b, "- `%s`：%d 种合法排序产生不同最终形状\n", group.Name, len(group.Shapes))
		case group.Classification == "unrecoverable":
			fmt.Fprintf(&b, "- `%s`：对基线没有任何合法排序\n", group.Name)
		case group.Classification == "blocked":
			fmt.Fprintf(&b, "- `%s`：依赖未定的前序组\n", group.Name)
		case group.Classification == "unresolved":
			fmt.Fprintf(&b, "- `%s`：超出穷举上限，未判定\n", group.Name)
		}
	}
	return b.String()
}

func hasUnresolved(groups []Group) bool {
	for _, group := range groups {
		if group.Unresolved {
			return true
		}
	}
	return false
}

func countClass(groups []Group, class string) int {
	count := 0
	for _, group := range groups {
		if group.Classification == class {
			count++
		}
	}
	return count
}

func classLabel(class string) string {
	switch class {
	case "unique":
		return "唯一可重放"
	case "ambiguous":
		return "多解，不可无歧义恢复"
	case "unrecoverable":
		return "无法恢复"
	case "blocked":
		return "受阻，无法判定"
	case "unresolved":
		return "未判定"
	default:
		return class
	}
}

func lineNumbers(rows []Row) []int {
	lines := make([]int, len(rows))
	for i, row := range rows {
		lines[i] = row.Line
	}
	return lines
}

func joinInts(values []int) string {
	parts := make([]string, len(values))
	for i, value := range values {
		parts[i] = fmt.Sprintf("%d", value)
	}
	return strings.Join(parts, ", ")
}

func inlineList(values []string) string {
	if len(values) == 0 {
		return "—"
	}
	parts := make([]string, len(values))
	for i, value := range values {
		parts[i] = "`" + value + "`"
	}
	return strings.Join(parts, ", ")
}
