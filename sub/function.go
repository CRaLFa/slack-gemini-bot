package sub

import (
	"bytes"
	"context"
	"encoding/gob"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/pubsub"
	"github.com/CRaLFa/slack-gemini-bot/pub"
	"github.com/GoogleCloudPlatform/functions-framework-go/functions"
	"github.com/cloudevents/sdk-go/v2/event"
	"github.com/google/generative-ai-go/genai"
	"github.com/samber/lo"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"google.golang.org/api/option"
)

type MessagePublishedData struct {
	Message pubsub.Message
}

var (
	slackBotToken string
	geminiAPIKey  string
	isDebug       bool
	botUser       string
)

func init() {
	slackBotToken = os.Getenv("SLACK_BOT_TOKEN")
	geminiAPIKey = os.Getenv("GEMINI_API_KEY")
	isDebug, _ = strconv.ParseBool(os.Getenv("DEBUG"))

	functions.CloudEvent("Subscribe", Subscribe)
}

func Subscribe(ctx context.Context, e event.Event) error {
	var msg MessagePublishedData
	if err := e.DataAs(&msg); err != nil {
		fmt.Println(err)
		return err
	}
	fmt.Printf("Received a message: %s\n", msg.Message.ID)

	buf := bytes.NewBuffer(msg.Message.Data)
	var event pub.APIInnerEvent
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

	gemini, err := genai.NewClient(ctx, option.WithAPIKey(geminiAPIKey))
	if err != nil {
		fmt.Println(err)
		return err
	}
	defer gemini.Close()
	model := gemini.GenerativeModel("gemini-1.5-flash")

	processEvent(ctx, &event, api, model)
	return nil
}

func processEvent(ctx context.Context, event *pub.APIInnerEvent, api *slack.Client, model *genai.GenerativeModel) {
	switch event.Type {
	case string(slackevents.AppMention):
		if isDebug {
			fmt.Printf("AppMentionEvent: %#v\n", event)
		}
		answer, blobs := generateAnswer(ctx, model, removeMention(event.Text), event.FileURLs)
		if answer == "" && len(blobs) <= 0 {
			return
		}
		if len(blobs) <= 0 {
			options := []slack.MsgOption{createBlocks(answer), slack.MsgOptionTS(event.TimeStamp)}
			postMessage(ctx, api, event.Channel, options)
		} else {
			uploadFile(ctx, api, event, answer, &blobs[0])
		}
	case string(slackevents.Message):
		if isDebug {
			fmt.Printf("MessageEvent: %#v\n", event)
		}
		if event.ThreadTimeStamp == "" {
			// メンションもしくはダイレクトメッセージ
			isMentionToBot := strings.Contains(event.Text, "<@"+botUser+">")
			if event.ChannelType == slack.TYPE_CHANNEL && !isMentionToBot {
				return
			}
			answer, blobs := generateAnswer(ctx, model, removeMention(event.Text), event.FileURLs)
			if answer == "" && len(blobs) <= 0 {
				return
			}
			if len(blobs) <= 0 {
				options := []slack.MsgOption{createBlocks(answer)}
				if isMentionToBot {
					options = append(options, slack.MsgOptionTS(event.TimeStamp))
				}
				postMessage(ctx, api, event.Channel, options)
			} else {
				uploadFile(ctx, api, event, answer, &blobs[0])
			}
		} else {
			params := &slack.GetConversationRepliesParameters{
				ChannelID: event.Channel,
				Timestamp: event.ThreadTimeStamp,
			}
			answer, blobs := generateChatAnswer(ctx, api, params, model, removeMention(event.Text), event.FileURLs)
			if answer == "" && len(blobs) <= 0 {
				return
			}
			if len(blobs) <= 0 {
				options := []slack.MsgOption{createBlocks(answer), slack.MsgOptionTS(event.ThreadTimeStamp)}
				postMessage(ctx, api, event.Channel, options)
			} else {
				uploadFile(ctx, api, event, answer, &blobs[0])
			}
		}
	default:
		fmt.Println("Unsupported innerEvent type:", event.Type)
	}
}

func removeMention(text string) string {
	mention := "<@" + botUser + ">"
	return strings.TrimSpace(strings.ReplaceAll(text, mention, ""))
}

func createBlocks(text string) slack.MsgOption {
	textBlock := slack.NewTextBlockObject(slack.MarkdownType, text, false, false)
	return slack.MsgOptionBlocks(slack.NewSectionBlock(textBlock, nil, nil))
}

func generateAnswer(
	ctx context.Context,
	model *genai.GenerativeModel,
	prompt string,
	fileURLs []string,
) (string, []genai.Blob) {
	if prompt == "" {
		return "", nil
	}
	parts := []genai.Part{genai.Text(prompt)}
	appendParts(&parts, getBlobs(ctx, fileURLs))
	res, err := model.GenerateContent(ctx, parts...)
	if err != nil {
		fmt.Println("Failed to get Gemini's response:", err)
		return "", nil
	}
	return joinResponse(res)
}

func generateChatAnswer(
	ctx context.Context,
	api *slack.Client,
	params *slack.GetConversationRepliesParameters,
	model *genai.GenerativeModel,
	prompt string,
	fileURLs []string,
) (string, []genai.Blob) {
	if prompt == "" {
		return "", nil
	}
	msgs, _, _, err := api.GetConversationRepliesContext(ctx, params)
	if err != nil {
		fmt.Println("Failed to get thread content:", err)
		return "", nil
	}
	if msgs[len(msgs)-2].User != botUser {
		return "", nil
	}
	if isDebug {
		for i, msg := range msgs {
			fmt.Printf("msgs[%d]: %#v\n", i, msg)
		}
	}
	chat := model.StartChat()
	chat.History = createChatHistory(ctx, msgs)
	parts := []genai.Part{genai.Text(prompt)}
	appendParts(&parts, getBlobs(ctx, fileURLs))
	res, err := chat.SendMessage(ctx, parts...)
	if err != nil {
		fmt.Println("Failed to get Gemini's response:", err)
		return "", nil
	}
	return joinResponse(res)
}

func postMessage(ctx context.Context, api *slack.Client, channel string, options []slack.MsgOption) {
	if _, _, err := api.PostMessageContext(ctx, channel, options...); err != nil {
		fmt.Println("Failed to post message:", err)
	}
}

func uploadFile(ctx context.Context, api *slack.Client, event *pub.APIInnerEvent, answer string, blob *genai.Blob) {
	buf := bytes.NewBuffer(blob.Data)
	name := fmt.Sprintf("file_%d.%s", time.Now().Unix(), filepath.Base(blob.MIMEType))
	params := slack.UploadFileV2Parameters{
		FileSize:        buf.Len(),
		Reader:          buf,
		Filename:        name,
		Title:           name,
		Channel:         event.Channel,
		ThreadTimestamp: lo.Ternary(event.ThreadTimeStamp == "", event.TimeStamp, event.ThreadTimeStamp),
	}
	if answer != "" {
		params.InitialComment = answer
	}
	if _, err := api.UploadFileV2Context(ctx, params); err != nil {
		fmt.Println("Failed to upload file:", err)
	}
}

func getBlobs(ctx context.Context, urls []string) []genai.Blob {
	var wg sync.WaitGroup
	wg.Add(len(urls))
	ch := make(chan []byte)
	for _, url := range urls {
		go fetchFile(ctx, url, &wg, ch)
	}
	go func() {
		wg.Wait()
		close(ch)
	}()
	var blobs []genai.Blob
	for data := range ch {
		blobs = append(blobs, genai.Blob{
			MIMEType: http.DetectContentType(data),
			Data:     data,
		})
	}
	return blobs
}

func appendParts[P genai.Part](sp *[]genai.Part, parts []P) {
	for _, p := range parts {
		*sp = append(*sp, p)
	}
}

func joinResponse(res *genai.GenerateContentResponse) (string, []genai.Blob) {
	reList := regexp.MustCompile(`(\n+\s*)\* `)
	replaceMarkdown := func(s string) string {
		if isDebug {
			fmt.Printf("%q\n", s)
		}
		s = reList.ReplaceAllString(s, "${1}- ")
		s = strings.ReplaceAll(s, "**", "*")
		return s
	}
	var strBuf []string
	var blobs []genai.Blob
	for _, cand := range res.Candidates {
		if cand == nil {
			continue
		}
		for _, part := range cand.Content.Parts {
			switch p := part.(type) {
			case genai.Text:
				strBuf = append(strBuf, replaceMarkdown(string(p)))
			case genai.Blob:
				blobs = append(blobs, p)
			default:
				continue
			}
		}
	}
	return strings.Join(strBuf, "\n"), blobs
}

func createChatHistory(ctx context.Context, msgs []slack.Message) []*genai.Content {
	return lo.Map(msgs[:len(msgs)-1], func(msg slack.Message, _ int) *genai.Content {
		parts := []genai.Part{genai.Text(removeMention(msg.Text))}
		if len(msg.Files) > 0 {
			urls := lo.Map(msg.Files, func(f slack.File, _ int) string {
				return f.URLPrivateDownload
			})
			appendParts(&parts, getBlobs(ctx, urls))
		}
		return &genai.Content{
			Parts: parts,
			Role:  lo.Ternary(msg.User == botUser, "model", "user"),
		}
	})
}

func fetchFile(ctx context.Context, url string, wg *sync.WaitGroup, ch chan []byte) {
	defer wg.Done()
	if url == "" {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		fmt.Println(err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+slackBotToken)
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != http.StatusOK {
		fmt.Println("Failed to fetch file data:", res.Status)
		return
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		fmt.Println(err)
		return
	}
	ch <- data
}
