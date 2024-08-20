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
	if _, err := api.AuthTest(); err != nil {
		log.Fatal(err)
	}
	socketClient := socketmode.New(api, socketmode.OptionDebug(*isDebug))

	ctx := context.Background()
	geminiClient, err := genai.NewClient(ctx, option.WithAPIKey(geminiApiKey))
	if err != nil {
		log.Fatal(err)
	}
	defer geminiClient.Close()

	model := geminiClient.GenerativeModel("gemini-1.5-flash")

	go processSocketEvent(socketClient, &ctx, model)
	socketClient.Run()
}

func processSocketEvent(client *socketmode.Client, ctx *context.Context, model *genai.GenerativeModel) {
	for envelope := range client.Events {
		switch envelope.Type {
		case socketmode.EventTypeEventsAPI:
			client.Ack(*envelope.Request)
			payload, ok := envelope.Data.(slackevents.EventsAPIEvent)
			if !ok {
				fmt.Printf("Not an Event API event: %+v\n", envelope)
				continue
			}
			client.Debugf("payload: %+v\n", payload)
			switch payload.Type {
			case slackevents.CallbackEvent:
				switch mention := payload.InnerEvent.Data.(type) {
				case *slackevents.AppMentionEvent:
					prompt := reMention.ReplaceAllLiteralString(mention.Text, "")
					client.Debugf("Received message: %s\n", prompt)
					res, err := model.GenerateContent(*ctx, genai.Text(prompt))
					if err != nil {
						fmt.Printf("Failed to get Gemini's response: %+v", err)
						continue
					}
					client.PostMessage(mention.Channel, slack.MsgOptionText(joinResponse(res), false), slack.MsgOptionTS(mention.TimeStamp))
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
