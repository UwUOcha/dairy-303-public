package botcore

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/UwUOcha/dairy-303-public/internal/api"
	"github.com/UwUOcha/dairy-303-public/internal/importdata"
	"github.com/UwUOcha/dairy-303-public/internal/store"
)

type featureSync struct{}

func (featureSync) EnsureRange(context.Context, int64, string, string) error { return nil }
func (featureSync) SyncMonth(context.Context, int64, int, int) error         { return nil }
func (featureSync) LastError() error                                         { return nil }

func TestPersonalNotificationsAndNextEndToEnd(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SaveGroupTree(ctx, importdata.GroupTree{Groups: []importdata.Group{{ID: 39, Name: "ГР-22", Active: true}}}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveSubgroups(ctx, 39, []importdata.Subgroup{{ID: 1, Name: "ГР-22/1"}, {ID: 2, Name: "ГР-22/2"}}); err != nil {
		t.Fatal(err)
	}
	u := store.User{Platform: "tg", ExtID: "1", GroupID: 39, SubgroupID: 1}.WithDefaults(180)
	if err := db.SaveUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(t.TempDir(), "api.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: api.NewServer(db, featureSync{}, time.UTC, 24*time.Hour, log).Handler()}
	go srv.Serve(ln)
	defer srv.Close()
	client := api.NewClient(sock)
	b := New(client, 180, log)
	sender := &fakeSender{}
	n := NewNotifier(b, client, sender, 1000, log)
	tomorrow := time.Now().UTC().AddDate(0, 0, 1)
	date := tomorrow.Format("2006-01-02")
	ms := importdata.MonthSchedule{GroupID: 39, Year: tomorrow.Year(), Month: int(tomorrow.Month()), LessonTimes: []importdata.LessonTime{{ID: 1, MinuteFrom: 600, MinuteTo: 690, Label: "10:00 - 11:30"}}, Lessons: []importdata.Lesson{
		{ID: 1, GroupID: 39, SubgroupID: 1, Date: date, LessonTimeID: 1, Discipline: "Химия", Classroom: "305"},
		{ID: 2, GroupID: 39, SubgroupID: 2, Date: date, LessonTimeID: 1, Discipline: "Физика", Classroom: "100"},
	}}
	save := func(enqueue bool) {
		t.Helper()
		if _, err := db.SaveMonth(ctx, ms); err != nil {
			t.Fatal(err)
		}
		if enqueue {
			if _, err := db.EnqueueChange(ctx, 39, ms.Year, ms.Month); err != nil {
				t.Fatal(err)
			}
		}
	}
	save(false)
	ms.Lessons[1].Classroom = "200"
	save(true)
	if err := n.drain(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sender.msgs) != 0 {
		t.Fatalf("another subgroup notification: %v", sender.msgs)
	}
	ms.Lessons[0].Classroom = "412"
	save(true)
	if err := n.drain(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sender.msgs) != 1 || !strings.Contains(sender.msgs[0], "305 → 412") {
		t.Fatalf("personal summary: %v", sender.msgs)
	}
	ms.Lessons[0].Classroom = "500"
	save(true)
	ms.Lessons[0].Classroom = "412"
	save(true)
	if err := n.drain(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sender.msgs) != 1 {
		t.Fatal("rollback sent a notification")
	}
	replies, err := b.Handle(ctx, Update{Platform: "tg", UserID: "1", Text: "/next"})
	if err != nil || len(replies) != 1 {
		t.Fatalf("next replies=%v err=%v", replies, err)
	}
	if !strings.Contains(replies[0].Text, "Химия") || strings.Contains(replies[0].Text, "Физика") || !strings.Contains(replies[0].Text, "10:00") {
		t.Fatal(replies[0].Text)
	}
	if replies[0].Keyboard.Rows[0][0].Data != "d:"+date {
		t.Fatalf("next day link: %+v", replies[0].Keyboard)
	}
	// The same action is reachable through the inline button.
	replies, err = b.Handle(ctx, Update{Platform: "tg", UserID: "1", Callback: cbNextLesson})
	if err != nil || !strings.Contains(replies[0].Text, "Химия") {
		t.Fatal("next callback failed")
	}
}
