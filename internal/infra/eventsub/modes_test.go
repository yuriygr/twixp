package eventsub

import (
	"encoding/json"
	"errors"
	"testing"

	"twixp/internal/domain"
)

// Пример payload из документации Twitch (channel.chat_settings.update).
const settingsUpdateJSON = `{
	"metadata": {"message_type": "notification"},
	"payload": {
		"subscription": {
			"type": "channel.chat_settings.update",
			"condition": {"broadcaster_user_id": "1337", "user_id": "9001"}
		},
		"event": {
			"broadcaster_user_id": "1337",
			"broadcaster_user_login": "cool_user",
			"broadcaster_user_name": "Cool_User",
			"emote_mode": true,
			"follower_mode": true,
			"follower_mode_duration_minutes": 10,
			"slow_mode": true,
			"slow_mode_wait_time_seconds": 10,
			"subscriber_mode": false,
			"unique_chat_mode": false
		}
	}
}`

const settingsAllOffJSON = `{
	"metadata": {"message_type": "notification"},
	"payload": {
		"subscription": {"type": "channel.chat_settings.update"},
		"event": {
			"broadcaster_user_id": "1337",
			"emote_mode": false,
			"follower_mode": false,
			"follower_mode_duration_minutes": null,
			"slow_mode": false,
			"slow_mode_wait_time_seconds": null,
			"subscriber_mode": false,
			"unique_chat_mode": false
		}
	}
}`

func mustEnvelope(t *testing.T, raw string) eventSubEnvelope {
	t.Helper()
	var env eventSubEnvelope
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatal(err)
	}
	return env
}

func TestDecodeChatSettingsUpdate(t *testing.T) {
	modes, broadcasterID, err := decodeChatSettingsUpdate(mustEnvelope(t, settingsUpdateJSON))
	if err != nil {
		t.Fatal(err)
	}
	want := domain.ChatModes{
		EmoteOnly:        true,
		FollowersOnly:    true,
		FollowersMinutes: 10,
		SlowMode:         true,
		SlowSeconds:      10,
	}
	if broadcasterID != "1337" || modes != want {
		t.Fatalf("получили %q %+v, ожидали 1337 %+v", broadcasterID, modes, want)
	}
}

func TestDecodeChatSettingsUpdateAllOff(t *testing.T) {
	modes, _, err := decodeChatSettingsUpdate(mustEnvelope(t, settingsAllOffJSON))
	if err != nil {
		t.Fatal(err)
	}
	if modes.Any() {
		t.Fatalf("null-длительности и выключенные режимы: %+v", modes)
	}
}

func TestDecodeChatSettingsUpdateMissingBroadcaster(t *testing.T) {
	if _, _, err := decodeChatSettingsUpdate(eventSubEnvelope{}); err == nil {
		t.Fatal("ожидали ошибку без broadcaster_user_id")
	}
}

// fakeSubs реализует SubscriptionManager ровно настолько, насколько
// нужно тестам режимов: остальные методы паникуют на nil-интерфейсе,
// если их вдруг вызовут.
type fakeSubs struct {
	SubscriptionManager
	modes domain.ChatModes
	err   error
}

func (f fakeSubs) GetChatModes(domain.Channel) (domain.ChatModes, error) { return f.modes, f.err }

func newTestHub(subs SubscriptionManager) (*Hub, *channelState) {
	ch := domain.Channel{ID: "1337", Name: "cool_user"}
	state := &channelState{channel: ch, modes: make(chan domain.ChatModes, messageBufferSize)}
	h := &Hub{subs: subs, channels: map[string]*channelState{ch.ID: state}}
	return h, state
}

func TestDispatchRoutesSettingsUpdateToModes(t *testing.T) {
	h, state := newTestHub(nil)

	h.dispatch(mustEnvelope(t, settingsUpdateJSON))

	select {
	case got := <-state.modes:
		if !got.EmoteOnly || got.FollowersMinutes != 10 {
			t.Fatalf("%+v", got)
		}
	default:
		t.Fatal("режимы не доставлены")
	}
	if !state.modesSeen {
		t.Fatal("modesSeen должен встать после события")
	}
}

func TestDispatchIgnoresUnknownChannel(t *testing.T) {
	h, state := newTestHub(nil)
	env := mustEnvelope(t, settingsUpdateJSON)
	env.Payload.Event.BroadcasterUserID = "другой"

	h.dispatch(env)

	if len(state.modes) != 0 {
		t.Fatal("событие чужого канала не должно попасть в этот")
	}
}

func TestInitialModesDelivered(t *testing.T) {
	want := domain.ChatModes{SubscribersOnly: true}
	h, state := newTestHub(fakeSubs{modes: want})

	h.loadInitialModes(state)

	select {
	case got := <-state.modes:
		if got != want {
			t.Fatalf("%+v", got)
		}
	default:
		t.Fatal("начальные режимы не доставлены")
	}
}

func TestInitialModesDoNotOverwriteNewerEvent(t *testing.T) {
	// Событие по подписке обогнало ответ на начальный запрос: устаревший
	// ответ не должен затереть более свежее состояние.
	h, state := newTestHub(fakeSubs{modes: domain.ChatModes{}})

	h.dispatch(mustEnvelope(t, settingsUpdateJSON))
	h.loadInitialModes(state)

	if len(state.modes) != 1 {
		t.Fatalf("в потоке %d значений, ожидали только событие", len(state.modes))
	}
	if got := <-state.modes; !got.EmoteOnly {
		t.Fatalf("осталось не свежее значение: %+v", got)
	}
}

func TestInitialModesAfterChannelDroppedIsSafe(t *testing.T) {
	h, state := newTestHub(fakeSubs{modes: domain.ChatModes{EmoteOnly: true}})
	state.messages = make(chan domain.ChatMessage)
	state.deletions = make(chan domain.MessageDeletion)

	h.dropChannel(state.channel.ID) // закрывает state.modes

	h.loadInitialModes(state) // не должно паниковать записью в закрытый канал
}

func TestInitialModesErrorIsNotFatal(t *testing.T) {
	h, state := newTestHub(fakeSubs{err: errors.New("нет сети")})

	h.loadInitialModes(state)

	if len(state.modes) != 0 || state.modesSeen {
		t.Fatal("при ошибке ничего не доставляется и флаг не ставится")
	}
}
