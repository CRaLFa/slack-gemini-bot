package pub

import (
	"bytes"
	"context"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"

	"cloud.google.com/go/pubsub"
	"github.com/GoogleCloudPlatform/functions-framework-go/functions"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
)

type ApiInnerEvent struct {
	Type            string
	Channel         string
	ChannelType     string
	User            string
	Text            string
	TimeStamp       string
	ThreadTimeStamp string
	FileUrls        []string
}

var (
	projectId string
	topicId   string
	isDebug   bool
)

func init() {
	projectId = os.Getenv("PROJECT_ID")
	topicId = os.Getenv("TOPIC_ID")
	isDebug, _ = strconv.ParseBool(os.Getenv("DEBUG"))

	functions.HTTP("Publish", Publish)
}

func Publish(w http.ResponseWriter, r *http.Request) {
	apiEvent := handleRequest(w, r)
	if apiEvent == nil {
		return
	}

	innerEvent := toApiInnerEvent(apiEvent)
	if innerEvent == nil {
		return
	}

	ctx := context.Background()

	client, err := pubsub.NewClient(ctx, projectId)
	if err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	defer client.Close()

	if err := publishTopic(client, &ctx, innerEvent); err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
	}
}

func handleRequest(w http.ResponseWriter, r *http.Request) *slackevents.EventsAPIEvent {
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		fmt.Println("No request body")
		w.WriteHeader(http.StatusBadRequest)
		return nil
	}
	event, err := slackevents.ParseEvent(json.RawMessage(body), slackevents.OptionNoVerifyToken())
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return nil
	}
	if event.Type == slackevents.URLVerification {
		var res slackevents.ChallengeResponse
		if err := json.Unmarshal(body, &res); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return nil
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(res.Challenge))
		return nil
	}
	return &event
}

func toApiInnerEvent(event *slackevents.EventsAPIEvent) *ApiInnerEvent {
	if event.Type != slackevents.CallbackEvent {
		fmt.Println("Unsupported event type:", event.Type)
		return nil
	}
	switch innerEvent := event.InnerEvent.Data.(type) {
	case *slackevents.AppMentionEvent:
		if isDebug {
			fmt.Printf("AppMentionEvent: %#v\n", innerEvent)
		}
		return nil
	case *slackevents.MessageEvent:
		if innerEvent.ChannelType == slack.TYPE_CHANNEL && innerEvent.ThreadTimeStamp == "" && !regexp.MustCompile(`<@\w+>`).MatchString(innerEvent.Text) {
			return nil
		}
		if isDebug {
			fmt.Printf("MessageEvent: %#v\n", innerEvent)
		}
		e := ApiInnerEvent{
			Type:            innerEvent.Type,
			User:            innerEvent.User,
			Text:            innerEvent.Text,
			TimeStamp:       innerEvent.TimeStamp,
			ThreadTimeStamp: innerEvent.ThreadTimeStamp,
			Channel:         innerEvent.Channel,
			ChannelType:     innerEvent.ChannelType,
		}
		for _, file := range innerEvent.Files {
			e.FileUrls = append(e.FileUrls, file.URLPrivateDownload)
		}
		return &e
	default:
		fmt.Println("Unsupported innerEvent type:", event.InnerEvent.Type)
		return nil
	}
}

func publishTopic(client *pubsub.Client, ctx *context.Context, innerEvent *ApiInnerEvent) error {
	buf := bytes.NewBuffer(nil)
	if err := gob.NewEncoder(buf).Encode(innerEvent); err != nil {
		return err
	}
	result := client.Topic(topicId).Publish(*ctx, &pubsub.Message{
		Data: buf.Bytes(),
	})
	msgId, err := result.Get(*ctx)
	if err != nil {
		return err
	}
	fmt.Printf("Published a message: %s\n", msgId)
	return nil
}
