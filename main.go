package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v3"
	"github.com/charmbracelet/lipgloss"
	"encoding/base64"
	"github.com/joho/godotenv"
)

var (
	titleStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("205"))

	dateStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("240"))

	cursorStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("46")) // green

	normalStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("252"))

	logoStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("99"))
)

/*
========================
CONFIG
========================
*/

const (
	addr     = ":2222"
	postsDir = "./posts"
)

/*
========================
DATA MODEL
========================
*/

type Post struct {
	Filename string
	Title    string
	Date     time.Time
	Content  string
}

/*
========================
SSH SERVER
========================
*/

func main() {
	config := &ssh.ServerConfig{
		NoClientAuth: true, // demo only
	}
	env := os.Getenv("APP_ENV")
	
	if env != "production" {

	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
	}

	}

	keyB64 := os.Getenv("SERVER_KEY")
if keyB64 == "" {
	log.Fatal("SERVER_KEY is empty")
}

keyBytes, err := base64.StdEncoding.DecodeString(keyB64)
if err != nil {
	log.Fatal("invalid base64 server key:", err)
}

	signer, err := ssh.ParsePrivateKey(keyBytes)
	if err != nil {
		log.Fatal(err)
	}

	config.AddHostKey(signer)

	l, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatal(err)
	}

	log.Println("SSH blog running on", addr)

	for {
		conn, err := l.Accept()
		if err != nil {
			continue
		}
		go handleConn(conn, config)
	}
}

func handleConn(nConn net.Conn, config *ssh.ServerConfig) {
	sshConn, chans, reqs, err := ssh.NewServerConn(nConn, config)
	if err != nil {
		return
	}
	defer sshConn.Close()

	go ssh.DiscardRequests(reqs)

	for ch := range chans {
		if ch.ChannelType() != "session" {
			ch.Reject(ssh.UnknownChannelType, "session only")
			continue
		}

		channel, requests, _ := ch.Accept()

		go func() {
			defer channel.Close()

			for req := range requests {
				switch req.Type {
				case "pty-req":
					req.Reply(true, nil)

				case "shell", "exec":
					req.Reply(true, nil)
					runTUI(channel)
					return
				}
			}
		}()
	}
}

/*
========================
BUBBLE TEA MODEL
========================
*/

type model struct {
	posts   []Post
	cursor  int
	viewing bool
	content string

	width  int
	height int
}

func initialModel() model {
	return model{
		posts: loadPosts(),
	}
}

func (m model) Init() tea.Cmd {
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case tea.KeyMsg:
		switch msg.String() {

		case "ctrl+c", "q":
			return m, tea.Quit

		case "up":
			if !m.viewing && m.cursor > 0 {
				m.cursor--
			}

		case "down":
			if !m.viewing && m.cursor < len(m.posts)-1 {
				m.cursor++
			}

		case "enter":
			if len(m.posts) > 0 {
				m.viewing = true
				m.content = m.posts[m.cursor].Content
			}
			return m, tea.ClearScreen

		case "esc", "backspace":
			m.viewing = false
			return m, tea.ClearScreen
		}
	}

	return m, nil
}

/*
========================
VIEW (NO GHOSTING)
========================
*/

func (m model) View() string {
	var b strings.Builder

	if m.viewing {
		b.WriteString("\n")
		b.WriteString(titleStyle.Render(m.posts[m.cursor].Title) + "\n\n")
		b.WriteString(m.content)
		b.WriteString("\n\n[esc/backspace to return]\n")
		return b.String()
	}

	logo := `
  |---\
  |   /\
  |  /  \
  |  \  /
  |   \/
  |---/
`

	b.WriteString(logoStyle.Render(logo))
	b.WriteString(titleStyle.Render("SSH MARKDOWN BLOG") + "\n")
	b.WriteString("sorted by date · SSH TUI\n\n")

	if len(m.posts) == 0 {
		b.WriteString("No posts found.\n")
		return b.String()
	}

	for i, p := range m.posts {
	cursor := " "
	if i == m.cursor {
		cursor = cursorStyle.Render(">")
	} else {
		cursor = " "
	}

	title := p.Title
	if title == "" {
		title = p.Filename
	}

	title = normalStyle.Render(title)

	date := ""
	if !p.Date.IsZero() {
		date = dateStyle.Render(p.Date.Format("2006-01-02"))
	}

	line := fmt.Sprintf("  %s %s  %s", cursor, title, date)
	b.WriteString(line + "\n")
}

	b.WriteString("\n↑/↓ navigate   enter open   q quit\n")

	return b.String()
}

/*
========================
TUI RUNNER
========================
*/

func runTUI(c ssh.Channel) {
	p := tea.NewProgram(
		initialModel(),
		tea.WithInput(c),
		tea.WithOutput(c),
		tea.WithAltScreen(),
	)

	_, _ = p.Run()
}

/*
========================
POST LOADING + FRONTMATTER
========================
*/

func loadPosts() []Post {
	files, err := os.ReadDir(postsDir)
	if err != nil {
		return nil
	}

	var posts []Post

	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".md") {
			continue
		}

		post, err := parsePost(filepath.Join(postsDir, f.Name()))
		if err == nil {
			posts = append(posts, post)
		}
	}

	sort.Slice(posts, func(i, j int) bool {
		// newest first; zero dates go last
		if posts[i].Date.IsZero() {
			return false
		}
		if posts[j].Date.IsZero() {
			return true
		}
		return posts[i].Date.After(posts[j].Date)
	})

	return posts
}

func parsePost(path string) (Post, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Post{}, err
	}

	content := string(data)

	post := Post{
		Filename: filepath.Base(path),
		Content:  content,
	}

	// no frontmatter
	if !strings.HasPrefix(content, "---") {
		return post, nil
	}

	parts := strings.SplitN(content, "---", 3)
	if len(parts) < 3 {
		return post, nil
	}

	metaRaw := parts[1]
	body := parts[2]

	var meta struct {
		Title string `yaml:"title"`
		Date  string `yaml:"date"`
	}

	_ = yaml.Unmarshal([]byte(metaRaw), &meta)

	post.Title = meta.Title
	post.Content = strings.TrimSpace(body)

	if meta.Date != "" {
		t, err := time.Parse("2006-01-02", meta.Date)
		if err == nil {
			post.Date = t
		}
	}

	return post, nil
}

/*
========================
UTIL
========================
*/

func readFile(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return "failed to read"
	}
	defer f.Close()

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, f)
	return buf.String()
}