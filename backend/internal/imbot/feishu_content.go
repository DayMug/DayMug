package imbot

// 飞书消息内容解析：把各类 message content JSON blob 拍平成纯文本 / 附件 key。
// 这里全是无 SDK 依赖的纯函数。

import (
	"encoding/json"
	"strings"
)

// parseFeishuText extracts the text field from a text-message content blob
// (`{"text":"@_user_1 hello"}`). Returns "" on malformed content.
func parseFeishuText(content string) string {
	var body struct {
		Text string `json:"text"`
	}
	if json.Unmarshal([]byte(content), &body) != nil {
		return ""
	}
	return body.Text
}

func parseFeishuImageKey(content string) string {
	var body struct {
		ImageKey string `json:"image_key"`
	}
	if json.Unmarshal([]byte(content), &body) != nil {
		return ""
	}
	return strings.TrimSpace(body.ImageKey)
}

func parseFeishuFile(content string) (key, name string) {
	var body struct {
		FileKey  string `json:"file_key"`
		FileName string `json:"file_name"`
	}
	if json.Unmarshal([]byte(content), &body) != nil {
		return "", ""
	}
	return strings.TrimSpace(body.FileKey), strings.TrimSpace(body.FileName)
}

type feishuPostElement struct {
	Tag      string `json:"tag"`
	Text     string `json:"text"`
	Href     string `json:"href"`
	UserID   string `json:"user_id"`
	UserName string `json:"user_name"`
	ImageKey string `json:"image_key"`
}

// parseFeishuPost flattens a rich-text (post) content blob — the message type
// 飞书 produces when text and images are pasted together — into plain text
// plus the embedded image keys. The bot's own at-element is dropped and
// reported via mentionedBot; other people's become inline @name. Returns
// zero values on malformed content.
func parseFeishuPost(content, botOpenID string) (text string, imageKeys []string, mentionedBot bool) {
	var body struct {
		Title   string                `json:"title"`
		Content [][]feishuPostElement `json:"content"`
	}
	if json.Unmarshal([]byte(content), &body) != nil {
		return "", nil, false
	}
	var lines []string
	if title := strings.TrimSpace(body.Title); title != "" {
		lines = append(lines, title)
	}
	for _, row := range body.Content {
		var parts []string
		for _, el := range row {
			switch el.Tag {
			case "text":
				parts = append(parts, el.Text)
			case "a":
				label := strings.TrimSpace(el.Text)
				switch {
				case label == "":
					parts = append(parts, el.Href)
				case el.Href != "":
					parts = append(parts, label+" ("+el.Href+")")
				default:
					parts = append(parts, label)
				}
			case "at":
				// Fail closed on an unknown bot open_id — see the matching gate
				// in (*FeishuConnector).inlineFeishuMentions.
				if botOpenID != "" && el.UserID == botOpenID {
					mentionedBot = true
					continue
				}
				if name := firstNonEmpty(el.UserName, el.UserID); name != "" {
					parts = append(parts, "@"+name)
				}
			case "img":
				if el.ImageKey != "" {
					imageKeys = append(imageKeys, el.ImageKey)
				}
			}
		}
		if line := strings.TrimSpace(strings.Join(parts, "")); line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n"), imageKeys, mentionedBot
}

// parseFeishuInteractive extracts user-visible text from both legacy and
// schema-2 cards. History is requested with raw_card_content so webhook alert
// cards can be supplied to the agent instead of an opaque JSON blob.
func parseFeishuInteractive(content string) string {
	var card any
	if json.Unmarshal([]byte(content), &card) != nil {
		return ""
	}
	lines := make([]string, 0, 8)
	collectFeishuCardText(card, &lines)
	return strings.Join(lines, "\n")
}

func collectFeishuCardText(value any, lines *[]string) {
	switch node := value.(type) {
	case []any:
		for _, child := range node {
			collectFeishuCardText(child, lines)
		}
	case map[string]any:
		// raw_card_content wraps the whole card as a JSON *string* under
		// json_card; without decoding it every app card reads as empty and the
		// history loader drops it — the alert a reply asks about vanishes.
		if raw, ok := node["json_card"].(string); ok {
			var card any
			if json.Unmarshal([]byte(raw), &card) == nil {
				collectFeishuCardText(card, lines)
			}
			return
		}
		tag, _ := node["tag"].(string)
		if tag == "markdown" || tag == "lark_md" || tag == "plain_text" {
			if content, ok := node["content"].(string); ok {
				appendFeishuCardLine(lines, content)
				return
			}
			// The json_card form nests a component's attributes under property.
			if property, ok := node["property"].(map[string]any); ok {
				if content, ok := property["content"].(string); ok {
					appendFeishuCardLine(lines, content)
					return
				}
			}
		}
		// Explicit ordering avoids random map iteration scrambling a card's
		// header and body. Unknown structural wrappers are traversed last.
		visited := map[string]bool{}
		for _, key := range []string{"header", "body", "elements", "property", "title", "text", "content"} {
			if child, ok := node[key]; ok {
				visited[key] = true
				if text, ok := child.(string); ok && (key == "title" || key == "text") {
					appendFeishuCardLine(lines, text)
					continue
				}
				collectFeishuCardText(child, lines)
			}
		}
		for key, child := range node {
			if !visited[key] {
				collectFeishuCardText(child, lines)
			}
		}
	}
}

func appendFeishuCardLine(lines *[]string, text string) {
	text = strings.TrimSpace(text)
	if text == "" || (len(*lines) > 0 && (*lines)[len(*lines)-1] == text) {
		return
	}
	*lines = append(*lines, text)
}
