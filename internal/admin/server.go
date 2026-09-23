package admin

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"github.com/UwUOcha/dairy-303-public/internal/profile"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/UwUOcha/dairy-303-public/internal/api"
	"github.com/UwUOcha/dairy-303-public/internal/config"
)

//go:embed assets
var assetsFS embed.FS

// Server — панель целиком: страница, её JSON и фоновый съём метрик.
type Server struct {
	cfg            config.Admin
	rasp           *api.Client
	sampler        *Sampler
	historySampler *Sampler
	history        *History
	log            *slog.Logger

	index *template.Template

	// last — последний удачный ответ raspd. Панель открывается и когда демон
	// расписания лежит: цифры будут вчерашние, но с честной пометкой — ровно
	// тот же приём, которым бот переживает падение источника.
	mu      sync.RWMutex
	last    *api.StatsResponse
	lastAt  time.Time
	lastErr string
}

// New собирает панель.
func New(cfg config.Admin, log *slog.Logger, history *History) (*Server, error) {
	index, err := template.ParseFS(assetsFS, "assets/index.html")
	if err != nil {
		return nil, err
	}
	return &Server{
		cfg:            cfg,
		rasp:           api.NewClient(cfg.SocketPath),
		sampler:        NewSampler(cfg.ProcPath, cfg.DiskPath),
		historySampler: NewSampler(cfg.ProcPath, cfg.DiskPath),
		history:        history,
		log:            log,
		index:          index,
	}, nil
}

// Handler собирает маршруты панели под её префиксом.
//
// Префикс не срезается прокси намеренно: caddy отдаёт путь как есть, а панель
// знает, где живёт. Иначе пришлось бы либо переписывать ссылки на лету, либо
// требовать от прокси конкретной директивы — и то и другое ломается тихо.
func (s *Server) SiteHandler() http.Handler {
	mux := http.NewServeMux()

	base := s.cfg.BasePath
	mux.HandleFunc(base+"/api/stats", s.handleStats)
	mux.HandleFunc(base+"/api/history", s.handleHistory)

	static, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		// Каталог зашит в бинарь через embed: его отсутствие — ошибка сборки,
		// а не среды, и обнаружиться она должна на первом же запуске.
		panic(err)
	}
	mux.Handle(base+"/static/", http.StripPrefix(base+"/static/", http.FileServer(http.FS(static))))

	mux.HandleFunc(base+"/", s.handleIndex)
	if base != "" {
		// "/admin" без хвостовой косой: браузер иначе разрешит относительные
		// ссылки от корня домена и уйдёт мимо панели.
		mux.HandleFunc(base, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, base+"/", http.StatusMovedPermanently)
		})
	}

	return mux
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	// ServeMux отдаёт "base/" всё, что не нашлось глубже: без этой проверки
	// опечатка в адресе показывала бы панель вместо 404.
	if r.URL.Path != s.cfg.BasePath+"/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Панель — витрина живых цифр: кэшировать её страницу значит однажды
	// смотреть на прошлый деплой и не понимать, почему ничего не меняется.
	w.Header().Set("Cache-Control", "no-store")
	if err := s.index.Execute(w, map[string]any{"WebMark": profile.Current().WebsiteMark(), "Base": s.cfg.BasePath, "University": profile.Current().University, "AppName": profile.Current().AppName, "Location": profile.Current().LocationLabel, "Config": publicConfig()}); err != nil {
		s.log.Error("не удалось отрисовать страницу панели", "ошибка", err)
	}
}

// View — то, что панель отдаёт браузеру одним ответом.
type View struct {
	Now  int64              `json:"now"`
	Host Host               `json:"host"`
	Rasp *api.StatsResponse `json:"rasp,omitempty"`
	// RaspAge — сколько секунд назад удался последний поход к демону. Ноль,
	// когда данные только что получены.
	RaspAge int64 `json:"rasp_age"`
	// RaspError — почему демон не ответил в этот раз. Данные при этом могут
	// быть непустыми: показывается прошлый снимок с пометкой.
	RaspError string `json:"rasp_error,omitempty"`
	TZOffset  int    `json:"tz_offset"`
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	view := s.view(r.Context())
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	window := 24 * time.Hour
	switch r.URL.Query().Get("range") {
	case "1h":
		window = time.Hour
	case "6h":
		window = 6 * time.Hour
	case "7d":
		window = 7 * 24 * time.Hour
	case "30d":
		window = 30 * 24 * time.Hour
	}

	samples, err := s.history.Range(r.Context(), window, 480)
	if err != nil {
		s.log.Error("не удалось прочитать историю", "ошибка", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"samples": samples})
}

// view собирает текущую картину: свежие метрики машины и последний снимок
// демона.
func (s *Server) view(ctx context.Context) View {
	return s.sampleView(ctx, s.sampler)
}

func (s *Server) sampleView(ctx context.Context, sampler *Sampler) View {
	host := sampler.Sample()
	stats, at, errText := s.stats(ctx)
	v := View{
		Now:       time.Now().Unix(),
		Host:      host,
		Rasp:      stats,
		RaspError: errText,
		TZOffset:  s.cfg.TZOffset,
	}
	if !at.IsZero() {
		v.RaspAge = int64(time.Since(at).Seconds())
	}
	if stats != nil {
		v.TZOffset = stats.TZOffset
	}
	return v
}

// stats ходит в raspd и запоминает удачный ответ.
func (s *Server) stats(ctx context.Context) (*api.StatsResponse, time.Time, string) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	resp, err := s.rasp.Stats(ctx)
	if err == nil {
		s.mu.Lock()
		s.last, s.lastAt, s.lastErr = &resp, time.Now(), ""
		s.mu.Unlock()
		return &resp, time.Now(), ""
	}

	s.mu.Lock()
	s.lastErr = err.Error()
	last, at, text := s.last, s.lastAt, s.lastErr
	s.mu.Unlock()
	return last, at, text
}

// SampleInterval — как часто панель кладёт точку в историю.
func (s *Server) SampleInterval() time.Duration { return s.cfg.SampleInterval }

// Sample снимает точку истории и записывает её.
//
// История считает CPU между своими замерами. Просмотры страницы не
// меняют начало этого интервала.
func (s *Server) Sample(ctx context.Context) error {
	view := s.sampleView(ctx, s.historySampler)

	sample := Sample{
		At:         time.Now().Unix(),
		CPUPercent: view.Host.CPUPercent,
		MemUsed:    view.Host.MemUsed,
		MemTotal:   view.Host.MemTotal,
		SwapUsed:   view.Host.SwapUsed,
		DiskUsed:   view.Host.DiskUsed,
		DiskTotal:  view.Host.DiskTotal,
		Load1:      view.Host.Load1,
	}
	if view.Rasp != nil {
		sample.Users = view.Rasp.Stats.Users.Total
		sample.UsersDay = view.Rasp.Stats.Users.Active1d
		sample.Lessons = view.Rasp.Stats.Content.Lessons
		sample.DBBytes = view.Rasp.Stats.Content.DBBytes
		sample.RaspMem = view.Rasp.Rasp.Sys
		if view.Rasp.Bot != nil {
			sample.BotMem = view.Rasp.Bot.Runtime.Sys
		}
	}
	return s.history.Add(ctx, sample)
}

// primeDelay — сколько ждать между первым, отбрасываемым замером и первой
// записанной точкой.
//
// Занятость процессора — это разница двух отсчётов, у первого разницы нет, и
// он неизбежно даёт ноль. Записать его значит навсегда оставить в истории
// провал в пол на каждом деплое. Пять секунд достаточно, чтобы разница стала
// осмысленной, и незаметны на фоне пятиминутного шага.
const primeDelay = 5 * time.Second

// Run крутит фоновый съём истории до отмены контекста.
func (s *Server) Run(ctx context.Context) {
	// Первый замер отбрасываем: он только заводит счётчик процессорных тиков,
	// от которого считается следующий.
	s.sampler.Sample()

	select {
	case <-ctx.Done():
		return
	case <-time.After(primeDelay):
	}

	// Дальше точку кладём сразу: иначе после перезапуска график пять минут
	// заканчивается там, где его застал прошлый процесс.
	if err := s.Sample(ctx); err != nil && ctx.Err() == nil {
		s.log.Warn("не удалось записать точку истории", "ошибка", err)
	}

	ticker := time.NewTicker(s.SampleInterval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.Sample(ctx); err != nil && !errors.Is(err, context.Canceled) {
				s.log.Warn("не удалось записать точку истории", "ошибка", err)
			}
		}
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// Handler cannot expose the former IP-only panel. Use web.WithAdmin with SiteHandler.
func (s *Server) Handler() http.Handler { return http.NotFoundHandler() }
func publicConfig() string              { b, _ := json.Marshal(profile.Current().Public()); return string(b) }
