package domain

import "testing"

func TestPointsRedemptionFromMessageType(t *testing.T) {
	cases := []struct {
		in   string
		want PointsRedemption
	}{
		{"channel_points_highlighted", RedeemedHighlight},
		{"channel_points_sub_only", RedeemedSubOnly},
		{"text", NoRedemption},
		{"user_intro", NoRedemption},
		{"", NoRedemption},
	}
	for _, c := range cases {
		if got := PointsRedemptionFromMessageType(c.in); got != c.want {
			t.Errorf("%q → %v, ожидали %v", c.in, got, c.want)
		}
	}
}

func TestPointsRedemptionLabel(t *testing.T) {
	if NoRedemption.Label() != "" {
		t.Error("у обычного сообщения баннера быть не должно")
	}
	if RedeemedHighlight.Label() == "" || RedeemedSubOnly.Label() == "" {
		t.Error("у оплаченных действий подпись обязательна")
	}
	if RedeemedHighlight.Label() == RedeemedSubOnly.Label() {
		t.Error("подписи разных действий должны отличаться")
	}
}
