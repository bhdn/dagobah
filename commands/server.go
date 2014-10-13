// Copyright © 2014 Steve Francia <spf@spf13.com>.
//
// Use of this source code is governed by an Apache2
// license that can be found in the LICENSE file.

package commands

import (
	"fmt"
	"html"
	"html/template"
	"log"
	"os"
	"strings"
	"time"

	"gopkg.in/mgo.v2/bson"

	"github.com/GeertJohan/go.rice"
	"github.com/gin-gonic/gin"
	"github.com/pilu/fresh/runner/runnerutils"
	"github.com/spf13/cast"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/gorilla/feeds"
)

var pLimit int

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Server for feeds",
	Long:  `Dagobah will serve all feeds listed in the config file.`,
	Run:   serverRun,
}

func init() {
	serverCmd.Flags().Int("port", 1138, "Port to run Dagobah server on")
	viper.BindPFlag("port", serverCmd.Flags().Lookup("port"))
	pLimit = viper.GetInt("page_limit")
}

func serverRun(cmd *cobra.Command, args []string) {
	Server()
}

func Server() {
	port := viper.GetString("port")

	r := gin.Default()

	if os.Getenv("DEV") != "" {
		r.Use(RunnerMiddleware())
	}

	templates := loadTemplates("full.html", "channels.html", "items.html", "main.html")
	r.SetHTMLTemplate(templates)

	r.GET("/ping", func(c *gin.Context) {
		c.String(200, "pong")
	})

	r.GET("/", homeRoute)
	r.GET("/atom", homeRouteAtom)
	r.GET("/post/*key", postRoute)
	r.GET("/search/*query", searchRoute)
	r.ServeFiles("/static/*filepath", rice.MustFindBox("static").HTTPBox())
	r.GET("/channel/*key", channelRoute)
	fmt.Println("Running on port:", port)
	r.Run(":" + port)
}

func RunnerMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if runnerutils.HasErrors() {
			runnerutils.RenderError(c.Writer)
			c.Abort(500)
		}
	}
}

func loadTemplates(list ...string) *template.Template {
	templateBox, err := rice.FindBox("templates")
	if err != nil {
		log.Fatal(err)
	}

	templates := template.New("")

	for _, x := range list {
		templateString, err := templateBox.String(x)
		if err != nil {
			log.Fatal(err)
		}

		// get file contents as string
		_, err = templates.New(x).Parse(templateString)
		if err != nil {
			log.Fatal(err)
		}
	}

	funcMap := template.FuncMap{
		"html":  ProperHtml,
		"title": func(a string) string { return strings.Title(a) },
	}

	templates.Funcs(funcMap)

	return templates
}

func ProperHtml(text string) template.HTML {
	if strings.Contains(text, "content:encoded>") || strings.Contains(text, "content/:encoded>") {
		text = html.UnescapeString(text)
	}
	return template.HTML(html.UnescapeString(template.HTMLEscapeString(text)))
}

func postRoute(c *gin.Context) {

	key := c.Params.ByName("key")

	if len(key) < 2 {
		four04(c, "Invalid Post")
		return
	}

	key = key[1:]

	var ps []Itm
	r := Items().Find(bson.M{"key": key}).Sort("-date").Limit(1)
	r.All(&ps)

	if len(ps) == 0 {
		four04(c, "Post not found")
		return
	}

	var posts []Itm
	results := Items().Find(bson.M{"date": bson.M{"$lte": ps[0].Date}}).Sort("-date").Limit(pLimit)
	results.All(&posts)

	channels := AllChannels()

	obj := gin.H{"title": ps[0].Title, "posts": posts, "items": posts, "channels": channels, "current": ps[0].Key}

	if strings.ToLower(c.Request.Header.Get("X-Requested-With")) == "xmlhttprequest" {
		c.HTML(200, "main.html", obj)
	} else {
		c.HTML(200, "full.html", obj)
	}
}

func CurrentPage(c *gin.Context) int {
	curPage := cast.ToInt(c.Request.FormValue("p"))
	if curPage < 1 {
		return 0
	}
	return curPage
}

func Offset(c *gin.Context) int {
	return pLimit * CurrentPage(c)
}

func four04(c *gin.Context, message string) {
	c.HTML(404, "full.html", gin.H{"message": message, "title": viper.GetString("title")})
}

func homeItems(offset int) (posts []Itm, nextPage int) {

	all := Items().Find(bson.M{}).Skip(offset)

	nextPage = 0
	remaining, _ := all.Count()
	if remaining > pLimit {
		curPage := offset / pLimit
		nextPage = curPage + 1
	}

	results := all.Sort("-date").Limit(pLimit)
	results.All(&posts)

	return
}

func homeRoute(c *gin.Context) {

	channels := AllChannels()

	posts, nextPage := homeItems(Offset(c))

	if len(posts) == 0 {
		four04(c, "No Articles")
		return
	}

	obj := gin.H{"title": viper.GetString("title"), "items": posts,
			"posts": posts, "channels": channels,
			"nextPage": nextPage}

	if strings.ToLower(c.Request.Header.Get("X-Requested-With")) == "xmlhttprequest" {
		c.HTML(200, "items.html", obj)
	} else {
		c.HTML(200, "full.html", obj)
	}
}

func homeRouteAtom(c *gin.Context) {
	now := time.Now()
	posts, _ := homeItems(Offset(c))

	feed := &feeds.Feed{
		Title:       viper.GetString("title"),
		Link:        &feeds.Link{Href: viper.GetString("url")},
		Description: viper.GetString("description"),
		Author:      &feeds.Author{viper.GetString("author"),
					viper.GetString("email")},
		Created:     now,
	}

	for _, post := range posts {

		//FIXME move this conversion elsewhere
		var link *feeds.Link
		rssLink := post.FirstLink()
		link = &feeds.Link{Href: rssLink.Href}

		item := &feeds.Item{
			Title:       post.Title,
			Link:        link,
			Description: post.Description,
			Created:     now,
		}
		feed.Items = append(feed.Items, item)
	}

	atom, _ := feed.ToAtom()

	raw := []byte(atom) // FIXME: encoding? errors?

	c.Data(200, "application/atom+xml", raw)
}

func searchRoute(c *gin.Context) {
	q := c.Params.ByName("query")
	if len(q) < 2 {
		four04(c, "Query is too short. Please try a longer query.")
		return
	}

	q = q[1:]

	channels := AllChannels()

	var posts []Itm
	// TODO need to send a PR to Gustavo with support for textscore sorting
	results := Items().Find(bson.M{"$text": bson.M{"$search": q}}).Skip(Offset(c)).Limit(pLimit)

	results.All(&posts)

	if len(posts) == 0 {
		four04(c, "No Articles for query '"+q+"'")
		return
	}

	obj := gin.H{"title": q, "header": q, "items": posts, "posts": posts, "channels": channels}

	if strings.ToLower(c.Request.Header.Get("X-Requested-With")) == "xmlhttprequest" {
		c.HTML(200, "items.html", obj)
	} else {
		c.HTML(200, "full.html", obj)
	}
}

func channelRoute(c *gin.Context) {
	key := c.Params.ByName("key")
	if len(key) < 2 {
		four04(c, "Channel Not Found")
		return
	}

	key = key[1:]

	var posts []Itm
	results := Items().Find(bson.M{"channelkey": key}).Skip(Offset(c)).Sort("-date").Limit(pLimit)
	results.All(&posts)

	if len(posts) == 0 {
		four04(c, "No Articles")
		return
	}

	channels := AllChannels()

	var currentChannel Chnl
	err := Channels().Find(bson.M{"key": key}).One(&currentChannel)
	if err != nil {
		if string(err.Error()) == "not found" {
			four04(c, "Channel not found")
			return
		} else {
			fmt.Println(err)
		}
	}

	obj := gin.H{"title": currentChannel.Title, "header": currentChannel.Title, "posts": posts, "items": posts, "channels": channels}

	if strings.ToLower(c.Request.Header.Get("X-Requested-With")) == "xmlhttprequest" {
		c.HTML(200, "items.html", obj)
	} else {
		c.HTML(200, "full.html", obj)
	}
}
