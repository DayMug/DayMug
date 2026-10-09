package imbot

import (
	"strings"

	"github.com/slack-go/slack"
)

// slackMessageText resolves the readable body of a Slack message.
//
// Integrations such as AWS Chatbot / Amazon Q post alarm cards with an empty
// `text` and put every fact — alarm name, region, instance id, thresholds — in
// Block Kit blocks hanging off a secondary attachment. Reading `text` alone
// yields an empty message, which the connector then drops, so the whole alarm
// disappears from the thread an operator is asking about.
//
// The rendering only kicks in when `text` is empty. A message that carries its
// own text already has the fallback Slack itself asks senders to provide, and
// its blocks are usually the same content again (plus link unfurls arrive as
// attachments); rendering those too would duplicate every ordinary message.
func slackMessageText(text string, blocks slack.Blocks, attachments []slack.Attachment) string {
	if strings.TrimSpace(text) != "" {
		return text
	}
	sections := make([]string, 0, 1+len(attachments))
	if rendered := renderSlackBlocks(blocks); rendered != "" {
		sections = append(sections, rendered)
	}
	for _, attachment := range attachments {
		if rendered := renderSlackAttachment(attachment); rendered != "" {
			sections = append(sections, rendered)
		}
	}
	return strings.Join(sections, "\n")
}

// renderSlackAttachment prefers the attachment's blocks and falls back to the
// legacy attachment fields, which older integrations still use exclusively.
func renderSlackAttachment(attachment slack.Attachment) string {
	if rendered := renderSlackBlocks(attachment.Blocks); rendered != "" {
		return rendered
	}
	parts := make([]string, 0, 5)
	appendSlackPart := func(value string) {
		if strings.TrimSpace(value) != "" {
			parts = append(parts, strings.TrimSpace(value))
		}
	}
	appendSlackPart(attachment.Pretext)
	appendSlackPart(attachment.Title)
	appendSlackPart(attachment.Text)
	for _, field := range attachment.Fields {
		appendSlackPart(strings.TrimSpace(field.Title + "\n" + field.Value))
	}
	if len(parts) == 0 {
		// Last resort: the plain-text summary Slack requires senders to set for
		// clients that cannot render the attachment at all.
		appendSlackPart(attachment.Fallback)
	}
	appendSlackPart(attachment.Footer)
	return strings.Join(parts, "\n")
}

// renderSlackBlocks flattens Block Kit into plain text. Interactive blocks
// (buttons, selects, inputs) carry no information about what happened, so they
// are skipped rather than rendered as orphaned labels.
func renderSlackBlocks(blocks slack.Blocks) string {
	parts := make([]string, 0, len(blocks.BlockSet))
	appendSlackPart := func(value string) {
		if strings.TrimSpace(value) != "" {
			parts = append(parts, strings.TrimSpace(value))
		}
	}
	for _, block := range blocks.BlockSet {
		switch typed := block.(type) {
		case *slack.SectionBlock:
			appendSlackPart(slackTextObject(typed.Text))
			for _, field := range typed.Fields {
				appendSlackPart(slackTextObject(field))
			}
		case *slack.HeaderBlock:
			appendSlackPart(slackTextObject(typed.Text))
		case *slack.ContextBlock:
			if typed.ContextElements.Elements == nil {
				continue
			}
			for _, element := range typed.ContextElements.Elements {
				if text, ok := element.(*slack.TextBlockObject); ok {
					appendSlackPart(slackTextObject(text))
				}
			}
		case *slack.RichTextBlock:
			appendSlackPart(renderSlackRichText(typed))
		case *slack.ImageBlock:
			// The image itself cannot be read here, but its title or alt text
			// often names the chart the alert is about.
			appendSlackPart(slackTextObject(typed.Title))
			if typed.Title == nil {
				appendSlackPart(typed.AltText)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func renderSlackRichText(block *slack.RichTextBlock) string {
	parts := make([]string, 0, len(block.Elements))
	for _, element := range block.Elements {
		section, ok := element.(*slack.RichTextSection)
		if !ok {
			continue
		}
		var line strings.Builder
		for _, item := range section.Elements {
			switch typed := item.(type) {
			case *slack.RichTextSectionTextElement:
				line.WriteString(typed.Text)
			case *slack.RichTextSectionLinkElement:
				line.WriteString(firstNonEmpty(typed.Text, typed.URL))
			case *slack.RichTextSectionUserElement:
				line.WriteString("<@" + typed.UserID + ">")
			case *slack.RichTextSectionChannelElement:
				line.WriteString("<#" + typed.ChannelID + ">")
			case *slack.RichTextSectionEmojiElement:
				line.WriteString(":" + typed.Name + ":")
			}
		}
		if strings.TrimSpace(line.String()) != "" {
			parts = append(parts, strings.TrimSpace(line.String()))
		}
	}
	return strings.Join(parts, "\n")
}

func slackTextObject(text *slack.TextBlockObject) string {
	if text == nil {
		return ""
	}
	return text.Text
}
