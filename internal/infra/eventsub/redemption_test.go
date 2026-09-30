package eventsub

import (
	"strings"
	"testing"

	"twixp/internal/domain"
)

// chatMessageJSON собирает минимальный payload channel.chat.message с
// заданным message_type.
func chatMessageJSON(messageType string) string {
	return `{
		"metadata": {"message_type": "notification", "message_timestamp": "2026-10-01T12:00:00.000000000Z"},
		"payload": {
			"subscription": {"type": "channel.chat.message"},
			"event": {
				"broadcaster_user_id": "1337",
				"broadcaster_user_login": "cool_user",
				"broadcaster_user_name": "Cool_User",
				"chatter_user_id": "9001",
				"chatter_user_login": "viewer",
				"chatter_user_name": "Viewer",
				"message_id": "m-1",
				"message": {"text": "привет", "fragments": []},
				"message_type": "` + messageType + `"
			}
		}
	}`
}

func TestChatMessageRedemptionFromMessageType(t *testing.T) {
	cases := []struct {
		messageType string
		want        domain.PointsRedemption
	}{
		{"text", domain.NoRedemption},
		{"channel_points_highlighted", domain.RedeemedHighlight},
		{"channel_points_sub_only", domain.RedeemedSubOnly},
		{"user_intro", domain.NoRedemption},
	}

	for _, c := range cases {
		t.Run(c.messageType, func(t *testing.T) {
			msg, broadcasterID, err := decodeChatMessage(mustEnvelope(t, chatMessageJSON(c.messageType)))
			if err != nil {
				t.Fatal(err)
			}
			if broadcasterID != "1337" || msg.Redemption != c.want {
				t.Fatalf("канал %q, Redemption %v; ожидали 1337 и %v", broadcasterID, msg.Redemption, c.want)
			}
			if !strings.Contains(msg.Text, "привет") {
				t.Fatalf("текст потерян: %q", msg.Text)
			}
		})
	}
}
