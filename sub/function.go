package sub

import (
	"bytes"
	"context"
	"encoding/gob"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"cloud.google.com/go/pubsub"
	"github.com/GoogleCloudPlatform/functions-framework-go/functions"
	"github.com/cloudevents/sdk-go/v2/event"
	"github.com/google/generative-ai-go/genai"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"google.golang.org/api/option"
)

type MessagePublishedData struct {
	Message pubsub.Message
}

type ApiInnerEvent struct {
	Type            string
	Channel         string
	ChannelType     string
	User            string
	Text            string
	TimeStamp       string
	ThreadTimeStamp string
}

var (
	slackBotToken string
	geminiApiKey  string
	isDebug       bool
	botUser       string
)

func init() {
	slackBotToken = os.Getenv("SLACK_BOT_TOKEN")
	geminiApiKey = os.Getenv("GEMINI_API_KEY")
	isDebug, _ = strconv.ParseBool(os.Getenv("DEBUG"))

	functions.CloudEvent("SlackGemini", SlackGemini)
}

func SlackGemini(ctx context.Context, e event.Event) error {
	var msg MessagePublishedData
	if err := e.DataAs(&msg); err != nil {
		fmt.Println(err)
		return err
	}
	fmt.Printf("Received a message: %s\n", msg.Message.ID)

	buf := bytes.NewBuffer(msg.Message.Data)
	var event ApiInnerEvent
	if err := gob.NewDecoder(buf).Decode(&event); err != nil {
		fmt.Println(err)
		return err
	}

	api := slack.New(slackBotToken, slack.OptionDebug(isDebug))
	res, err := api.AuthTest()
	if err != nil {
		fmt.Println(err)
		return err
	}
	botUser = res.UserID
	if event.User == botUser {
		return nil
	}

	gemini, err := genai.NewClient(ctx, option.WithAPIKey(geminiApiKey))
	if err != nil {
		fmt.Println(err)
		return err
	}
	defer gemini.Close()
	model := gemini.GenerativeModel("gemini-1.5-flash")

	processEvent(&event, &ctx, api, model)
	return nil
}

func processEvent(event *ApiInnerEvent, ctx *context.Context, api *slack.Client, model *genai.GenerativeModel) {
	reMention := regexp.MustCompile(`<@\w+>`)
	switch slackevents.EventsAPIType(event.Type) {
	case slackevents.AppMention:
		if isDebug {
			fmt.Printf("AppMentionEvent: %#v\n", event)
		}
		prompt := strings.TrimSpace(reMention.ReplaceAllLiteralString(event.Text, ""))
		answer := generateAnswer(ctx, model, prompt)
		if answer == "" {
			return
		}
		api.PostMessageContext(*ctx, event.Channel, *createBlocks(answer), slack.MsgOptionTS(event.TimeStamp))
	case slackevents.Message:
		if isDebug {
			fmt.Printf("MessageEvent: %#v\n", event)
		}
		prompt := strings.TrimSpace(reMention.ReplaceAllLiteralString(event.Text, ""))
		var options []slack.MsgOption
		if event.ThreadTimeStamp == "" {
			answer := generateAnswer(ctx, model, prompt)
			if answer == "" {
				return
			}
			options = append(options, *createBlocks(answer))
			if reMention.MatchString(event.Text) {
				options = append(options, slack.MsgOptionTS(event.TimeStamp))
			}
		} else {
			params := &slack.GetConversationRepliesParameters{
				ChannelID: event.Channel,
				Timestamp: event.ThreadTimeStamp,
			}
			answer := generateChatAnswer(ctx, api, params, model, prompt)
			if answer == "" {
				return
			}
			options = append(options, *createBlocks(answer), slack.MsgOptionTS(event.ThreadTimeStamp))
		}
		api.PostMessageContext(*ctx, event.Channel, options...)
	default:
		fmt.Println("Unsupported innerEvent type:", event.Type)
	}
}

func generateAnswer(ctx *context.Context, model *genai.GenerativeModel, prompt string) string {
	if prompt == "" {
		return ""
	}
	res, err := model.GenerateContent(*ctx, genai.Text(prompt))
	if err != nil {
		fmt.Println("Failed to get Gemini's response:", err)
		return ""
	}
	return joinResponse(res)
}

func generateChatAnswer(
	ctx *context.Context,
	api *slack.Client,
	params *slack.GetConversationRepliesParameters,
	model *genai.GenerativeModel,
	prompt string,
) string {
	if prompt == "" {
		return ""
	}
	msgs, _, _, err := api.GetConversationRepliesContext(*ctx, params)
	if err != nil {
		fmt.Println("Failed to get thread content:", err)
		return ""
	}
	if msgs[len(msgs)-2].User != botUser {
		return ""
	}
	if isDebug {
		for i, msg := range msgs {
			fmt.Printf("msgs[%d]: %#v\n", i, msg)
		}
	}
	chat := model.StartChat()
	chat.History = createChatHistory(msgs)
	res, err := chat.SendMessage(*ctx, genai.Text(prompt))
	if err != nil {
		fmt.Println("Failed to get Gemini's response:", err)
		return ""
	}
	return joinResponse(res)
}

func createBlocks(text string) *slack.MsgOption {
	textBlock := slack.NewTextBlockObject(slack.MarkdownType, text, false, false)
	blocks := slack.MsgOptionBlocks(slack.NewSectionBlock(textBlock, nil, nil))
	return &blocks
}

func createChatHistory(msgs []slack.Message) []*genai.Content {
	getRole := func(msg slack.Message) string {
		if msg.User == botUser {
			return "model"
		} else {
			return "user"
		}
	}
	history := []*genai.Content{}
	for _, msg := range msgs[:len(msgs)-1] {
		content := &genai.Content{
			Parts: []genai.Part{
				genai.Text(msg.Text),
			},
			Role: getRole(msg),
		}
		history = append(history, content)
	}
	return history
}

func joinResponse(res *genai.GenerateContentResponse) string {
	reList := regexp.MustCompile(`(\n+\s*)\* `)
	replaceMarkdown := func(s string) string {
		if isDebug {
			fmt.Printf("%#v\n", s)
		}
		s = reList.ReplaceAllString(s, "${1}- ")
		s = strings.Replace(s, "**", "*", -1)
		return s
	}
	var buf []string
	for _, cand := range res.Candidates {
		if cand != nil {
			for _, part := range cand.Content.Parts {
				buf = append(buf, replaceMarkdown(fmt.Sprintf("%v", part)))
			}
		}
	}
	return strings.Join(buf, "\n")
}
