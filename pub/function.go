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

	"cloud.google.com/go/pubsub"
	"github.com/GoogleCloudPlatform/functions-framework-go/functions"
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
}

var (
	projectId string
	topicId   string
)

func init() {
	projectId = os.Getenv("PROJECT_ID")
	topicId = os.Getenv("TOPIC_ID")

	functions.HTTP("SlackGemini", SlackGemini)
}

func SlackGemini(w http.ResponseWriter, r *http.Request) {
	apiEvent := handleRequest(w, r)
	if apiEvent == nil {
		return
	}

	innerEvent := toApiInnerEvent(apiEvent)
	if innerEvent == nil {
		w.WriteHeader(http.StatusBadRequest)
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
	body, err := io.ReadAll(r.Body)
	if err != nil {
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
	e := ApiInnerEvent{}
	switch innerEvent := event.InnerEvent.Data.(type) {
	case *slackevents.AppMentionEvent:
		e.Type = innerEvent.Type
		e.User = innerEvent.User
		e.Text = innerEvent.Text
		e.TimeStamp = innerEvent.TimeStamp
		e.Channel = innerEvent.Channel
	case *slackevents.MessageEvent:
		if innerEvent.ChannelType == "channel" && innerEvent.ThreadTimeStamp == "" {
			return nil
		}
		e.Type = innerEvent.Type
		e.User = innerEvent.User
		e.Text = innerEvent.Text
		e.TimeStamp = innerEvent.TimeStamp
		e.ThreadTimeStamp = innerEvent.ThreadTimeStamp
		e.Channel = innerEvent.Channel
		e.ChannelType = innerEvent.ChannelType
	default:
		fmt.Println("Unsupported innerEvent type:", event.InnerEvent.Type)
		return nil
	}
	return &e
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
