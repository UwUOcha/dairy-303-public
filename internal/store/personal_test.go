package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/UwUOcha/dairy-303-public/internal/importdata"
)

func TestPersonalQueueBaselineAndConcurrentAck(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	fixNow(t, "2026-09-01")
	u := User{Platform: "tg", ExtID: "1", GroupID: 232}.WithDefaults(180)
	if err := db.SaveUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	month := importdata.MonthSchedule{GroupID: 232, Year: 2026, Month: 9, LessonTimes: testTimes, Lessons: []importdata.Lesson{{ID: 1, Date: "2026-09-10", LessonTimeID: 1, Discipline: "Химия", GroupID: 232, Classroom: "A"}}}
	save := func(room string) {
		t.Helper()
		month.Lessons[0].Classroom = room
		if _, err := db.SaveMonth(ctx, month); err != nil {
			t.Fatal(err)
		}
	}
	enqueue := func() {
		t.Helper()
		if _, err := db.EnqueueChange(ctx, 232, 2026, 9); err != nil {
			t.Fatal(err)
		}
	}
	take := func() OutboxItem {
		t.Helper()
		items, err := db.TakeOutbox(ctx, "tg", 10)
		if err != nil || len(items) != 1 {
			t.Fatalf("items=%v err=%v", items, err)
		}
		return items[0]
	}
	baseline := func(it OutboxItem) string {
		t.Helper()
		var p ChangePayload
		if err := json.Unmarshal([]byte(it.Payload), &p); err != nil {
			t.Fatal(err)
		}
		if !p.Personal || len(p.Before) != 1 {
			t.Fatalf("payload=%+v", p)
		}
		return p.Before[0].Before[0].Classroom
	}
	save("A")
	save("B")
	enqueue()
	old := take()
	if baseline(old) != "A" {
		t.Fatal("initial baseline")
	}
	// Same day changes while the first message is in flight.
	save("C")
	enqueue()
	newer := take()
	if old.Payload == newer.Payload {
		t.Fatal("revision unchanged")
	}
	if err := db.OutboxDone(ctx, old.ID, old.Payload); err != nil {
		t.Fatal(err)
	}
	take() // stale acknowledgement must not delete the replacement
	if err := db.OutboxDone(ctx, newer.ID, newer.Payload); err != nil {
		t.Fatal(err)
	}
	save("D")
	enqueue()
	if baseline(take()) != "C" {
		t.Fatal("new notification reused historical baseline")
	}
}

func TestPersonalBaselineSurvivesSharedFlowUpdate(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	fixNow(t, "2026-09-01")
	ms := importdata.MonthSchedule{GroupID: 1, Year: 2026, Month: 9, LessonTimes: testTimes, Lessons: []importdata.Lesson{{ID: 99, Date: "2026-09-10", LessonTimeID: 1, Discipline: "Общая лекция", GroupID: 1, Classroom: "A", Audience: importdata.AudienceFlow}}}
	for _, group := range []int64{1, 2} {
		ms.GroupID = group
		if _, err := db.SaveMonth(ctx, ms); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.SaveUser(ctx, User{Platform: "tg", ExtID: "2", GroupID: 2}.WithDefaults(180)); err != nil {
		t.Fatal(err)
	}
	ms.Lessons[0].Classroom = "B"
	for _, group := range []int64{1, 2} {
		ms.GroupID = group
		if _, err := db.SaveMonth(ctx, ms); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.EnqueueChange(ctx, 2, 2026, 9); err != nil {
		t.Fatal(err)
	}
	items, err := db.TakeOutbox(ctx, "tg", 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("%v %v", items, err)
	}
	var p ChangePayload
	if err := json.Unmarshal([]byte(items[0].Payload), &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Before) != 1 || len(p.Before[0].Before) != 1 || p.Before[0].Before[0].Classroom != "A" {
		t.Fatalf("shared update erased baseline: %+v", p)
	}
}
