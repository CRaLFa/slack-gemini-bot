package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"

	"github.com/google/generative-ai-go/genai"
	"github.com/joho/godotenv"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
	"google.golang.org/api/option"
)

var (
	isDebug   = flag.Bool("d", false, "enable debug mode")
	reMention = regexp.MustCompile(`<@\w+>`)
	botUser   string
)

func main() {
	flag.Parse()

	if err := godotenv.Load(".env.yaml"); err != nil {
		log.Fatal("Error loading .env file")
	}
	slackBotToken := os.Getenv("SLACK_BOT_TOKEN")
	slackAppToken := os.Getenv("SLACK_APP_TOKEN")
	geminiApiKey := os.Getenv("GEMINI_API_KEY")

	api := slack.New(slackBotToken, slack.OptionAppLevelToken(slackAppToken), slack.OptionDebug(*isDebug))
	res, err := api.AuthTest()
	if err != nil {
		log.Fatal(err)
	}
	botUser = res.UserID
	socketClient := socketmode.New(api, socketmode.OptionDebug(*isDebug))

	ctx := context.Background()

	geminiClient, err := genai.NewClient(ctx, option.WithAPIKey(geminiApiKey))
	if err != nil {
		log.Fatal(err)
	}
	defer geminiClient.Close()

	model := geminiClient.GenerativeModel("gemini-1.5-flash")

	go processSocketEvent(&ctx, api, socketClient, model)
	socketClient.Run()
}

func processSocketEvent(ctx *context.Context, api *slack.Client, client *socketmode.Client, model *genai.GenerativeModel) {
	for envelope := range client.Events {
		switch envelope.Type {
		case socketmode.EventTypeEventsAPI:
			client.Ack(*envelope.Request)
			payload, ok := envelope.Data.(slackevents.EventsAPIEvent)
			if !ok {
				fmt.Printf("Not an Event API event: %+v\n", envelope)
				continue
			}
			switch payload.Type {
			case slackevents.CallbackEvent:
				switch event := payload.InnerEvent.Data.(type) {
				case *slackevents.AppMentionEvent:
					client.Debugf("AppMentionEvent: %+v\n", event)
					prompt := strings.TrimSpace(reMention.ReplaceAllLiteralString(event.Text, ""))
					answer := generateAnswer(ctx, model, prompt)
					if answer == "" {
						continue
					}
					client.PostMessageContext(*ctx, event.Channel, slack.MsgOptionText(answer, false), slack.MsgOptionTS(event.TimeStamp))
				case *slackevents.MessageEvent:
					client.Debugf("MessageEvent: %+v\n", event)
					if event.User == botUser || (event.ChannelType == "channel" && event.ThreadTimeStamp == "") {
						continue
					}
					prompt := strings.TrimSpace(reMention.ReplaceAllLiteralString(event.Text, ""))
					var answer string
					var options []slack.MsgOption
					if event.ThreadTimeStamp == "" {
						answer = generateAnswer(ctx, model, prompt)
						if answer == "" {
							continue
						}
						options = append(options, slack.MsgOptionText(answer, false))
						if reMention.MatchString(event.Text) {
							options = append(options, slack.MsgOptionTS(event.TimeStamp))
						}
					} else {
						params := &slack.GetConversationRepliesParameters{
							ChannelID: event.Channel,
							Timestamp: event.ThreadTimeStamp,
						}
						answer = generateChatAnswer(ctx, api, params, model, prompt)
						if answer == "" {
							continue
						}
						options = append(options, slack.MsgOptionText(answer, false), slack.MsgOptionTS(event.ThreadTimeStamp))
					}
					client.PostMessageContext(*ctx, event.Channel, options...)
				default:
					fmt.Printf("Unsupported innerEvent type: %s\n", payload.InnerEvent.Type)
				}
			default:
				fmt.Printf("Unsupported payload type: %s\n", payload.Type)
			}
		default:
			fmt.Printf("Unsupported event type: %s\n", envelope.Type)
		}
	}
}

func generateAnswer(ctx *context.Context, model *genai.GenerativeModel, prompt string) string {
	if prompt == "" {
		return ""
	}
	res, err := model.GenerateContent(*ctx, genai.Text(prompt))
	if err != nil {
		fmt.Printf("Failed to get Gemini's response: %+v", err)
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
		fmt.Printf("Failed to get thread content: %+v", err)
		return ""
	}
	chat := model.StartChat()
	chat.History = createChatHistory(msgs)
	res, err := chat.SendMessage(*ctx, genai.Text(prompt))
	if err != nil {
		fmt.Printf("Failed to get Gemini's response: %+v", err)
		return ""
	}
	return joinResponse(res)
}

func createChatHistory(msgs []slack.Message) []*genai.Content {
	history := []*genai.Content{}
	for _, msg := range msgs {
		content := &genai.Content{
			Parts: []genai.Part{
				genai.Text(msg.Text),
			},
			Role: "user",
		}
		history = append(history, content)
	}
	return history
}

func joinResponse(res *genai.GenerateContentResponse) string {
	var buf []string
	for _, cand := range res.Candidates {
		if cand != nil {
			for _, part := range cand.Content.Parts {
				buf = append(buf, fmt.Sprintf("%v", part))
			}
		}
	}
	return strings.Join(buf, "\n")
}
