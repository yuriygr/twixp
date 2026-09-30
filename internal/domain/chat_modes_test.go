package domain

import (
	"reflect"
	"testing"
)

func TestChatModesLabels(t *testing.T) {
	cases := []struct {
		name string
		in   ChatModes
		want []string
	}{
		{"свободный чат", ChatModes{}, nil},
		{"только смайлики", ChatModes{EmoteOnly: true}, []string{"Только смайлики"}},
		{"подписчики", ChatModes{SubscribersOnly: true}, []string{"Только подписчики"}},
		{"фолловеры без срока", ChatModes{FollowersOnly: true}, []string{"Только фолловеры"}},
		{"фолловеры 10 минут", ChatModes{FollowersOnly: true, FollowersMinutes: 10}, []string{"Только фолловеры (10 мин)"}},
		{"фолловеры 2 часа", ChatModes{FollowersOnly: true, FollowersMinutes: 120}, []string{"Только фолловеры (2 ч)"}},
		{"фолловеры 30 дней", ChatModes{FollowersOnly: true, FollowersMinutes: 43200}, []string{"Только фолловеры (30 д)"}},
		{"фолловеры 90 минут", ChatModes{FollowersOnly: true, FollowersMinutes: 90}, []string{"Только фолловеры (90 мин)"}},
		{"медленный 30 с", ChatModes{SlowMode: true, SlowSeconds: 30}, []string{"Медленный режим: 30 с"}},
		{"медленный 2 мин", ChatModes{SlowMode: true, SlowSeconds: 120}, []string{"Медленный режим: 2 мин"}},
		{"медленный без паузы", ChatModes{SlowMode: true}, []string{"Медленный режим"}},
		{"уникальные", ChatModes{UniqueOnly: true}, []string{"Только уникальные сообщения"}},
		{"поля-значения без флага режима игнорируются", ChatModes{FollowersMinutes: 10, SlowSeconds: 30}, nil},
		{
			"всё сразу, в фиксированном порядке",
			ChatModes{UniqueOnly: true, SlowMode: true, SlowSeconds: 5, FollowersOnly: true, SubscribersOnly: true, EmoteOnly: true},
			[]string{"Только смайлики", "Только подписчики", "Только фолловеры", "Медленный режим: 5 с", "Только уникальные сообщения"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.in.Labels()
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("Labels() = %q, ожидали %q", got, c.want)
			}
			if c.in.Any() != (len(c.want) > 0) {
				t.Fatalf("Any() = %v при %d подписях", c.in.Any(), len(c.want))
			}
		})
	}
}
