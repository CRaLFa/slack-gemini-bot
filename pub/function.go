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
	"github.com/jinzhu/copier"
	"github.com/samber/lo"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
)

type APIInnerEvent struct {
	Type            string
	Channel         string
	ChannelType     string
	User            string
	Text            string
	TimeStamp       string
	ThreadTimeStamp string
	FileURLs        []string
}

var (
	projectID string
	topicID   string
	isDebug   bool
)

func init() {
	projectID = os.Getenv("PROJECT_ID")
	topicID = os.Getenv("TOPIC_ID")

	var err error
	isDebug, err = strconv.ParseBool(os.Getenv("DEBUG"))
	if err != nil {
		panic(err)
	}

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

	client, err := pubsub.NewClient(ctx, projectID)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	defer client.Close()

	if err := publishTopic(ctx, client, innerEvent); err != nil {
		fmt.Fprintln(os.Stderr, err)
		w.WriteHeader(http.StatusInternalServerError)
	}
}

func handleRequest(w http.ResponseWriter, r *http.Request) *slackevents.EventsAPIEvent {
	defer r.Body.Close()

	body, err := io.ReadAll(r.Body)
	if err != nil {
		fmt.Fprintln(os.Stderr, "No request body")
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
		fmt.Fprint(w, res.Challenge)
		return nil
	}

	return &event
}

func toApiInnerEvent(event *slackevents.EventsAPIEvent) *APIInnerEvent {
	if event.Type != slackevents.CallbackEvent {
		fmt.Fprintln(os.Stderr, "Unsupported event type:", event.Type)
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
		e := APIInnerEvent{}
		copier.Copy(&e, &innerEvent)
		e.FileURLs = lo.Map(innerEvent.Files, func(f slackevents.File, _ int) string {
			return f.URLPrivateDownload
		})
		return &e
	default:
		fmt.Fprintln(os.Stderr, "Unsupported innerEvent type:", event.InnerEvent.Type)
		return nil
	}
}

func publishTopic(ctx context.Context, client *pubsub.Client, innerEvent *APIInnerEvent) error {
	buf := new(bytes.Buffer)
	if err := gob.NewEncoder(buf).Encode(innerEvent); err != nil {
		return err
	}

	result := client.Topic(topicID).Publish(ctx, &pubsub.Message{
		Data: buf.Bytes(),
	})
	msgID, err := result.Get(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("Published a message: %s\n", msgID)
	return nil
}
