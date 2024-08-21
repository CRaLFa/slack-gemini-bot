package function

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/GoogleCloudPlatform/functions-framework-go/functions"
	"github.com/google/generative-ai-go/genai"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"google.golang.org/api/option"
)

var (
	slackBotToken string
	geminiApiKey  string
	reMention     = regexp.MustCompile(`<@\w+>`)
	botUser       string
)

func init() {
	slackBotToken = os.Getenv("SLACK_BOT_TOKEN")
	geminiApiKey = os.Getenv("GEMINI_API_KEY")

	functions.HTTP("SlackGemini", SlackGemini)
}

func SlackGemini(w http.ResponseWriter, r *http.Request) {
	apiEvent := handleRequest(w, r)
	if apiEvent == nil {
		return
	}

	api := slack.New(slackBotToken, slack.OptionDebug(true))
	res, err := api.AuthTest()
	if err != nil {
		log.Fatal(err)
	}
	botUser = res.UserID

	ctx := context.Background()

	geminiClient, err := genai.NewClient(ctx, option.WithAPIKey(geminiApiKey))
	if err != nil {
		log.Fatal(err)
	}
	defer geminiClient.Close()

	model := geminiClient.GenerativeModel("gemini-1.5-flash")

	processApiEvent(apiEvent, &ctx, api, model)
}

func handleRequest(w http.ResponseWriter, r *http.Request) *slackevents.EventsAPIEvent {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return nil
	}
	apiEvent, err := slackevents.ParseEvent(json.RawMessage(body), slackevents.OptionNoVerifyToken())
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return nil
	}
	if apiEvent.Type == slackevents.URLVerification {
		var res *slackevents.ChallengeResponse
		err := json.Unmarshal(body, &res)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return nil
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(res.Challenge))
		return nil
	}
	return &apiEvent
}

func processApiEvent(apiEvent *slackevents.EventsAPIEvent, ctx *context.Context, api *slack.Client, model *genai.GenerativeModel) {
	switch apiEvent.Type {
	case slackevents.CallbackEvent:
		switch event := apiEvent.InnerEvent.Data.(type) {
		case *slackevents.AppMentionEvent:
			log.Printf("AppMentionEvent: %+v\n", event)
			prompt := strings.TrimSpace(reMention.ReplaceAllLiteralString(event.Text, ""))
			answer := generateAnswer(ctx, model, prompt)
			if answer == "" {
				return
			}
			api.PostMessageContext(*ctx, event.Channel, slack.MsgOptionText(answer, false), slack.MsgOptionTS(event.TimeStamp))
		case *slackevents.MessageEvent:
			log.Printf("MessageEvent: %+v\n", event)
			if event.User == botUser || (event.ChannelType == "channel" && event.ThreadTimeStamp == "") {
				return
			}
			prompt := strings.TrimSpace(reMention.ReplaceAllLiteralString(event.Text, ""))
			var answer string
			var options []slack.MsgOption
			if event.ThreadTimeStamp == "" {
				answer = generateAnswer(ctx, model, prompt)
				if answer == "" {
					return
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
					return
				}
				options = append(options, slack.MsgOptionText(answer, false), slack.MsgOptionTS(event.ThreadTimeStamp))
			}
			api.PostMessageContext(*ctx, event.Channel, options...)
		default:
			log.Printf("Unsupported innerEvent type: %s\n", apiEvent.InnerEvent.Type)
		}
	default:
		log.Printf("Unsupported apiEvent type: %s\n", apiEvent.Type)
	}
}

func generateAnswer(ctx *context.Context, model *genai.GenerativeModel, prompt string) string {
	if prompt == "" {
		return ""
	}
	res, err := model.GenerateContent(*ctx, genai.Text(prompt))
	if err != nil {
		log.Printf("Failed to get Gemini's response: %+v", err)
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
		log.Printf("Failed to get thread content: %+v", err)
		return ""
	}
	chat := model.StartChat()
	chat.History = createChatHistory(msgs)
	res, err := chat.SendMessage(*ctx, genai.Text(prompt))
	if err != nil {
		log.Printf("Failed to get Gemini's response: %+v", err)
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
