package main

// Game cards (user 2026-10-14: "I want to read a description, a review, and like/dislike and add to wish list of any
// game it shows; search the web for info on games I look up").
//
//   GET  /api/game/search?q=      find a game by name (Steam store; CheapShark when Steam has no such title)
//   GET  /api/game?appid=&title=  one card: description, genres, Metacritic, Steam reviews (summary + a few real ones),
//                                 a Wikipedia summary, every store's price, and the user's own like/wish mark
//   GET  /api/games/marks         the user's wish list, likes and dislikes
//   POST /api/games/mark          set like/dislike and/or wish list for a game
//
// "The web" here is Steam's public store pages, Wikipedia and CheapShark: no key needed, no account, nothing sent but
// the game's name. The marks live on this PC (games.json) and Lucille's chat prompt carries a short taste summary, so
// she can say "you wish-listed that" or "you didn't like the last one".

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type GameMark struct {
	AppID   int    `json:"appid,omitempty"`
	Name    string `json:"name"`
	Image   string `json:"image,omitempty"`
	Like    string `json:"like,omitempty"` // "like" | "dislike" | ""
	Wish    bool   `json:"wish"`
	Added   string `json:"added"`
	Updated string `json:"updated"`
}

type ReviewSample struct {
	Up    bool   `json:"up"`
	Text  string `json:"text"`
	Hours int    `json:"hours"`
}

type Shot struct {
	Thumb string `json:"thumb"`
	Full  string `json:"full"`
}

// steamImage accepts only images served from Steam's own CDN, so a card can never point the page at somewhere else.
func steamImage(u string) bool {
	p, err := url.Parse(u)
	return err == nil && p.Scheme == "https" && (strings.HasSuffix(p.Host, ".steamstatic.com") || p.Host == "steamstatic.com")
}

type GameCard struct {
	AppID       int            `json:"appid,omitempty"`
	Key         string         `json:"key"`
	Name        string         `json:"name"`
	Image       string         `json:"image,omitempty"`
	Description string         `json:"description,omitempty"`
	Release     string         `json:"release,omitempty"`
	Developers  []string       `json:"developers,omitempty"`
	Genres      []string       `json:"genres,omitempty"`
	Metacritic  int            `json:"metacritic,omitempty"`
	Price       string         `json:"price,omitempty"`
	ReviewWord  string         `json:"review_word,omitempty"` // "Very Positive"
	ReviewPct   int            `json:"review_pct,omitempty"`
	ReviewTotal int            `json:"review_total,omitempty"`
	Samples     []ReviewSample `json:"review_samples,omitempty"`
	Screenshots []Shot         `json:"screenshots,omitempty"`
	WebTitle    string         `json:"web_title,omitempty"`
	WebExtract  string         `json:"web_extract,omitempty"`
	WebURL      string         `json:"web_url,omitempty"`
	Prices      []Deal         `json:"prices,omitempty"`
	LowestEver  string         `json:"lowest_ever,omitempty"`
	StoreURL    string         `json:"store_url,omitempty"`
	SearchURL   string         `json:"search_url"`
	Notes       []string       `json:"notes,omitempty"` // which sources did not answer
	Mark        *GameMark      `json:"mark,omitempty"`
}

var (
	gamesMu  sync.Mutex
	htmlTags = regexp.MustCompile(`<[^>]*>`)
)

func gameKey(appid int, title string) string {
	if appid > 0 {
		return strconv.Itoa(appid)
	}
	return "t:" + slugOf(title)
}

func plainHTML(s string) string {
	return strings.TrimSpace(spaceRuns.ReplaceAllString(html.UnescapeString(htmlTags.ReplaceAllString(s, " ")), " "))
}

// ── marks ───────────────────────────────────────────────────────

func (a *App) gamesPath() string { return filepath.Join(a.cfg.DataDir, "games.json") }

func (a *App) readMarks() map[string]*GameMark {
	m := map[string]*GameMark{}
	if b, err := os.ReadFile(a.gamesPath()); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func (a *App) writeMarks(m map[string]*GameMark) error {
	if err := os.MkdirAll(a.cfg.DataDir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(a.gamesPath(), b, 0o600)
}

func sortedMarks(m map[string]*GameMark) []*GameMark {
	out := make([]*GameMark, 0, len(m))
	for _, g := range m {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Wish != out[j].Wish {
			return out[i].Wish
		}
		return out[i].Updated > out[j].Updated
	})
	return out
}

// gamesDigest is the taste summary Lucille's prompt carries.
func (a *App) gamesDigest() string {
	var wish, like, dis []string
	for _, g := range sortedMarks(a.readMarks()) {
		if g.Wish {
			wish = append(wish, g.Name)
		}
		switch g.Like {
		case "like":
			like = append(like, g.Name)
		case "dislike":
			dis = append(dis, g.Name)
		}
	}
	cut := func(s []string) string {
		if len(s) > 10 {
			s = s[:10]
		}
		return strings.Join(s, ", ")
	}
	var parts []string
	if len(wish) > 0 {
		parts = append(parts, "wish list: "+cut(wish))
	}
	if len(like) > 0 {
		parts = append(parts, "liked: "+cut(like))
	}
	if len(dis) > 0 {
		parts = append(parts, "disliked: "+cut(dis))
	}
	if len(parts) == 0 {
		return ""
	}
	return "\nThe user's own game marks, set in the Games tab (use them when they talk about games; don't invent others): " + strings.Join(parts, "; ") + ".\n"
}

// ── the sources ─────────────────────────────────────────────────

type steamHit struct {
	AppID     int    `json:"appid"`
	Name      string `json:"name"`
	Image     string `json:"image,omitempty"`
	Price     string `json:"price,omitempty"`
	Metascore string `json:"metascore,omitempty"`
}

func (a *App) steamSearch(ctx context.Context, q string) ([]steamHit, error) {
	var r struct {
		Items []struct {
			ID        int    `json:"id"`
			Name      string `json:"name"`
			TinyImage string `json:"tiny_image"`
			Metascore string `json:"metascore"`
			Price     *struct {
				Final    int    `json:"final"`
				Currency string `json:"currency"`
			} `json:"price"`
		} `json:"items"`
	}
	if err := getJSON(ctx, "https://store.steampowered.com/api/storesearch/?l=english&cc=us&term="+url.QueryEscape(q), &r); err != nil {
		return nil, err
	}
	var out []steamHit
	for _, it := range r.Items {
		h := steamHit{AppID: it.ID, Name: it.Name, Image: it.TinyImage, Metascore: it.Metascore}
		if it.Price != nil {
			h.Price = fmt.Sprintf("$%.2f", float64(it.Price.Final)/100)
		}
		out = append(out, h)
	}
	return out, nil
}

func (a *App) steamDetails(ctx context.Context, id int, c *GameCard) error {
	var raw map[string]struct {
		Success bool `json:"success"`
		Data    struct {
			Name       string                         `json:"name"`
			ShortDesc  string                         `json:"short_description"`
			Header     string                         `json:"header_image"`
			Developers []string                       `json:"developers"`
			Genres     []struct{ Description string } `json:"genres"`
			Release    struct{ Date string }          `json:"release_date"`
			Metacritic *struct{ Score int }           `json:"metacritic"`
			Price      *struct {
				Final string `json:"final_formatted"`
			} `json:"price_overview"`
			IsFree      bool `json:"is_free"`
			Screenshots []struct {
				Thumb string `json:"path_thumbnail"`
				Full  string `json:"path_full"`
			} `json:"screenshots"`
		} `json:"data"`
	}
	if err := getJSON(ctx, "https://store.steampowered.com/api/appdetails?l=english&appids="+strconv.Itoa(id), &raw); err != nil {
		return err
	}
	e, ok := raw[strconv.Itoa(id)]
	if !ok || !e.Success {
		return fmt.Errorf("Steam has no store page for app %d", id)
	}
	d := e.Data
	c.Name, c.Image, c.Description = d.Name, d.Header, plainHTML(d.ShortDesc)
	c.Developers, c.Release = d.Developers, d.Release.Date
	for _, g := range d.Genres {
		c.Genres = append(c.Genres, g.Description)
	}
	if d.Metacritic != nil {
		c.Metacritic = d.Metacritic.Score
	}
	for _, sh := range d.Screenshots {
		if len(c.Screenshots) < 16 && steamImage(sh.Thumb) && steamImage(sh.Full) {
			c.Screenshots = append(c.Screenshots, Shot{sh.Thumb, sh.Full})
		}
	}
	switch {
	case d.IsFree:
		c.Price = "Free"
	case d.Price != nil:
		c.Price = d.Price.Final
	}
	return nil
}

func (a *App) steamReviews(ctx context.Context, id int, c *GameCard) error {
	var r struct {
		Summary struct {
			Desc  string `json:"review_score_desc"`
			Pos   int    `json:"total_positive"`
			Neg   int    `json:"total_negative"`
			Total int    `json:"total_reviews"`
		} `json:"query_summary"`
		Reviews []struct {
			Up     bool   `json:"voted_up"`
			Review string `json:"review"`
			Author struct {
				Forever int `json:"playtime_forever"`
			} `json:"author"`
		} `json:"reviews"`
	}
	u := fmt.Sprintf("https://store.steampowered.com/appreviews/%d?json=1&language=english&purchase_type=all&filter=all&num_per_page=4", id)
	if err := getJSON(ctx, u, &r); err != nil {
		return err
	}
	c.ReviewWord, c.ReviewTotal = r.Summary.Desc, r.Summary.Total
	if r.Summary.Pos+r.Summary.Neg > 0 {
		c.ReviewPct = r.Summary.Pos * 100 / (r.Summary.Pos + r.Summary.Neg)
	}
	for _, rv := range r.Reviews {
		t := clip(plainHTML(rv.Review), 320)
		if len(t) > 20 {
			c.Samples = append(c.Samples, ReviewSample{Up: rv.Up, Text: t, Hours: rv.Author.Forever / 60})
		}
	}
	return nil
}

var wikiUA = "lucille-assistant/" + version + " (personal assistant; https://github.com/cledford955-maker)"

func wikiGet(ctx context.Context, u string, v any) error {
	req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	req.Header.Set("User-Agent", wikiUA)
	resp, err := httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// wikipedia finds the article for a game and keeps its summary, but only when its title really is that game.
func wikipedia(ctx context.Context, name string, c *GameCard) error {
	var s struct {
		Query struct {
			Search []struct{ Title string } `json:"search"`
		} `json:"query"`
	}
	if err := wikiGet(ctx, "https://en.wikipedia.org/w/api.php?action=query&list=search&format=json&srlimit=3&srsearch="+url.QueryEscape(name+" video game"), &s); err != nil {
		return err
	}
	want := strings.ToLower(name)
	for _, hit := range s.Query.Search {
		if !strings.Contains(strings.ToLower(hit.Title), want) && !strings.Contains(want, strings.ToLower(strings.SplitN(hit.Title, " (", 2)[0])) {
			continue
		}
		var sum struct {
			Title, Extract string
			URLs           struct {
				Desktop struct{ Page string } `json:"desktop"`
			} `json:"content_urls"`
		}
		if err := wikiGet(ctx, "https://en.wikipedia.org/api/rest_v1/page/summary/"+url.PathEscape(strings.ReplaceAll(hit.Title, " ", "_")), &sum); err != nil {
			return err
		}
		c.WebTitle, c.WebExtract, c.WebURL = sum.Title, clip(sum.Extract, 700), sum.URLs.Desktop.Page
		return nil
	}
	return fmt.Errorf("no Wikipedia article for %q", name)
}

func (a *App) gamePrices(ctx context.Context, appid int, title string, c *GameCard) error {
	var games []struct{ GameID, External, SteamAppID, Thumb string }
	q := cheapshark + "/games?limit=1&title=" + url.QueryEscape(title)
	if appid > 0 {
		q = cheapshark + "/games?steamAppID=" + strconv.Itoa(appid)
	}
	if err := getJSON(ctx, q, &games); err != nil || len(games) == 0 {
		return fmt.Errorf("no store prices found")
	}
	var detail struct {
		Info              struct{ Title string }
		CheapestPriceEver struct{ Price string }
		Deals             []struct{ StoreID, DealID, Price, RetailPrice, Savings string }
	}
	if err := getJSON(ctx, cheapshark+"/games?id="+games[0].GameID, &detail); err != nil {
		return err
	}
	for _, d := range detail.Deals {
		c.Prices = append(c.Prices, toDeal(csDeal{Title: detail.Info.Title, StoreID: d.StoreID, DealID: d.DealID, SalePrice: d.Price, NormalPrice: d.RetailPrice, Savings: d.Savings}))
	}
	if detail.CheapestPriceEver.Price != "" {
		c.LowestEver = "$" + detail.CheapestPriceEver.Price
	}
	if c.Image == "" {
		c.Image = games[0].Thumb
	}
	return nil
}

func (a *App) buildCard(ctx context.Context, appid int, title string) *GameCard {
	c := &GameCard{AppID: appid, Name: title}
	var wg sync.WaitGroup
	var mu sync.Mutex
	run := func(label string, f func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := f(); err != nil {
				mu.Lock()
				c.Notes = append(c.Notes, label+": "+err.Error())
				mu.Unlock()
			}
		}()
	}
	if appid > 0 {
		run("Steam page", func() error { return a.steamDetails(ctx, appid, c) })
		run("Steam reviews", func() error { return a.steamReviews(ctx, appid, c) })
		c.StoreURL = fmt.Sprintf("https://store.steampowered.com/app/%d", appid)
	}
	wg.Wait() // the name from Steam makes the Wikipedia and price lookups exact
	if c.Name == "" {
		c.Name = title
	}
	run("Wikipedia", func() error { return wikipedia(ctx, c.Name, c) })
	run("Store prices", func() error { return a.gamePrices(ctx, appid, c.Name, c) })
	wg.Wait()
	sort.Slice(c.Prices, func(i, j int) bool {
		x, _ := strconv.ParseFloat(c.Prices[i].SalePrice, 64)
		y, _ := strconv.ParseFloat(c.Prices[j].SalePrice, 64)
		return x < y
	})
	c.Key = gameKey(appid, c.Name)
	c.SearchURL = "https://duckduckgo.com/?q=" + url.QueryEscape(c.Name+" game review")
	return c
}

// ── HTTP ────────────────────────────────────────────────────────

func (a *App) handleGameSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" || len(q) > 80 {
		writeErr(w, http.StatusBadRequest, "Add ?q=Hades (80 characters at most).")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	hits, err := a.steamSearch(ctx, q)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": "Steam's store search did not answer."})
		return
	}
	if len(hits) > 8 {
		hits = hits[:8]
	}
	marks := a.readMarks()
	type row struct {
		steamHit
		Mark *GameMark `json:"mark,omitempty"`
	}
	rows := make([]row, 0, len(hits))
	for _, h := range hits {
		rows = append(rows, row{h, marks[gameKey(h.AppID, h.Name)]})
	}
	writeJSON(w, map[string]any{"ok": true, "q": q, "results": rows, "search_url": "https://duckduckgo.com/?q=" + url.QueryEscape(q+" game")})
}

func (a *App) handleGame(w http.ResponseWriter, r *http.Request) {
	appid, _ := strconv.Atoi(r.URL.Query().Get("appid"))
	title := strings.TrimSpace(r.URL.Query().Get("title"))
	if appid < 0 || len(title) > 120 || (appid == 0 && title == "") {
		writeErr(w, http.StatusBadRequest, "Add ?appid=1145360 or ?title=Hades.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if appid == 0 { // a title from a deals list: find its Steam page first
		if hits, err := a.steamSearch(ctx, title); err == nil {
			for _, h := range hits {
				if slugOf(h.Name) == slugOf(title) {
					appid = h.AppID
					break
				}
			}
		}
	}
	v, _ := a.cache.get(fmt.Sprintf("game:%d:%s", appid, strings.ToLower(title)), 2*time.Hour, func() (any, error) {
		return a.buildCard(ctx, appid, title), nil
	})
	card := *(v.(*GameCard))
	card.Mark = a.readMarks()[card.Key]
	writeJSON(w, map[string]any{"ok": true, "game": card})
}

func (a *App) handleGameMarks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"ok": true, "games": sortedMarks(a.readMarks())})
}

// POST /api/games/mark {"appid": 1145360, "name": "Hades", "image": "...", "like": "like"|"dislike"|"", "wish": true}
// Leave a field out to leave it as it is.
func (a *App) handleGameMark(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireHuman(w, r); !ok {
		return
	}
	var req struct {
		AppID int
		Name  string
		Image string
		Like  *string
		Wish  *bool
	}
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Name) == "" || req.AppID < 0 || len(req.Name) > 120 {
		writeErr(w, http.StatusBadRequest, `Send {"appid": 1145360, "name": "Hades", "like": "like", "wish": true}.`)
		return
	}
	if req.Like != nil && *req.Like != "like" && *req.Like != "dislike" && *req.Like != "" {
		writeErr(w, http.StatusBadRequest, `like must be "like", "dislike" or "".`)
		return
	}
	key, now := gameKey(req.AppID, req.Name), time.Now().Format(time.RFC3339)
	gamesMu.Lock()
	defer gamesMu.Unlock()
	marks := a.readMarks()
	g := marks[key]
	if g == nil {
		g = &GameMark{AppID: req.AppID, Name: strings.TrimSpace(req.Name), Added: now}
		marks[key] = g
	}
	if req.Image != "" && strings.HasPrefix(req.Image, "https://") && len(req.Image) < 300 {
		g.Image = req.Image
	}
	if req.Like != nil {
		g.Like = *req.Like
	}
	if req.Wish != nil {
		g.Wish = *req.Wish
	}
	g.Updated = now
	if !g.Wish && g.Like == "" { // nothing left to remember about it
		delete(marks, key)
		g = &GameMark{AppID: req.AppID, Name: req.Name}
	}
	if err := a.writeMarks(marks); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "key": key, "mark": g})
}
