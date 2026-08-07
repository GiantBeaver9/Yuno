package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func main() {
	// Load a local .env if present; never overrides vars already in the env.
	_ = godotenv.Load()

	if len(os.Args) > 1 && os.Args[1] == "mcp" {
		runMCP()
		return
	}
	runServer()
}

func runServer() {
	router := gin.Default()

	if store, err := openStore(dbPath()); err != nil {
		log.Printf("price tracker disabled: %v", err)
	} else {
		defer store.Close()
		registerPriceRoutes(router, store)
	}

	router.GET("/search/google", handleGoogleSearch)
	router.GET("/search/google/big", handleMegaSearch)
	router.GET("/search/google/summary", handleMegaSearchReduced)
	router.GET("/search/wikipedia", handleWikipediaSearch)
	router.GET("/search/youtube", handleYouTubeSearch)
	router.GET("/page", handleDirectPageGrab)
	router.GET("/news", handleNews)
	router.GET("/summarize-sites", handleMySites)
	router.POST("/summarize-sites", handleMySitesInstructed)
	router.POST("/email", handleSendEmail)
	router.POST("/download", handleDownloadAudio)
	router.GET("/models", handleListModels)
	router.GET("/getworddefinition", handleGetWordDefinition)

	router.Run(":8080")
}

func runMCP() {
	s := server.NewMCPServer(
		"go-search",
		"1.0.0",
	)

	googleTool := mcp.NewTool("search-google",
		mcp.WithDescription("Search Google for a query, returning the first result as text and the other links on the 1st page"),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("Search query sent to google"),
		),
		mcp.WithString("model",
			mcp.Description("Optional model id for summarization; defaults to LLM_MODEL"),
		),
	)

	bigGoogleTool := mcp.NewTool("search-google-big",
		mcp.WithDescription("Search Google, then fetch and summarize the top result pages. Returns a URL-led summary per page"),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("Search query sent to google"),
		),
		mcp.WithNumber("max_pages",
			mcp.Description("Max pages to fetch and summarize (default 5)"),
		),
		mcp.WithString("model",
			mcp.Description("Optional model id for summarization; defaults to LLM_MODEL"),
		),
	)

	googleSummaryTool := mcp.NewTool("search-google-summary",
		mcp.WithDescription("Like search-google-big, but also reduces every page summary into one combined answer"),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("Search query sent to google"),
		),
		mcp.WithNumber("max_pages",
			mcp.Description("Max pages to fetch and summarize (default 5)"),
		),
		mcp.WithString("model",
			mcp.Description("Optional model id for summarization; defaults to LLM_MODEL"),
		),
	)

	wikipediaTool := mcp.NewTool("search-wikipedia",
		mcp.WithDescription("Search Wikipedia for a query, returning first result in text"),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("Search query sent to wikipedia"),
		),
		mcp.WithString("model",
			mcp.Description("Optional model id for summarization; defaults to LLM_MODEL"),
		),
	)

	youtubeTool := mcp.NewTool("search-youtube",
		mcp.WithDescription("Search YouTube and return a list of videos (title + direct watch URL) to open or hand off"),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("Search query sent to YouTube"),
		),
		mcp.WithNumber("max_results",
			mcp.Description("Max videos to return (default 10)"),
		),
	)

	pageGrabTool := mcp.NewTool("get-page",
		mcp.WithDescription("Grab the text and links from a page given a URL"),
		mcp.WithString("url",
			mcp.Required(),
			mcp.Description("URL of the page to get/summarize"),
		),
		mcp.WithString("model",
			mcp.Description("Optional model id for summarization; defaults to LLM_MODEL"),
		),
	)

	summarizeNewsDayTool := mcp.NewTool("summarize-news",
		mcp.WithDescription("Summarize News using the pages in the MCP server array"),
		mcp.WithArray("newsSources",
			mcp.Required(),
			mcp.Description("Array of URLs to use to summarise news events of the day"),
			mcp.WithStringItems(),
		),
		mcp.WithString("model",
			mcp.Description("Optional model id for summarization; defaults to LLM_MODEL"),
		),
	)

	summarizeMySitesTool := mcp.NewTool("summarize-my-sites",
		mcp.WithDescription("Summarize a set of pages the caller provides (any websites). Each page can carry its own instruction, e.g. summarize Kotaku's homepage while checking Nintendo for new game announcements"),
		mcp.WithArray("sites",
			mcp.Required(),
			mcp.Description("Pages to scrape and summarize. Each item is {url, instruction}; instruction is optional and falls back to a generic summary when omitted"),
			mcp.Items(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"url": map[string]any{
						"type":        "string",
						"description": "Page URL to scrape and summarize",
					},
					"instruction": map[string]any{
						"type":        "string",
						"description": "What to look for or how to summarize this specific page (optional)",
					},
				},
				"required": []any{"url"},
			}),
		),
		mcp.WithString("model",
			mcp.Description("Optional model id for summarization; defaults to LLM_MODEL"),
		),
	)

	sendEmailTool := mcp.NewTool("send-email",
		mcp.WithDescription("Send a plain-text email (e.g. a summary or digest). SMTP is configured server-side via env vars"),
		mcp.WithArray("to",
			mcp.Required(),
			mcp.Description("Recipient email addresses"),
			mcp.WithStringItems(),
		),
		mcp.WithString("name",
			mcp.Description("Recipient name for the greeting, e.g. \"Dear {name},\". Optional"),
		),
		mcp.WithString("subject",
			mcp.Required(),
			mcp.Description("Email subject line"),
		),
		mcp.WithString("body",
			mcp.Required(),
			mcp.Description("Plain-text body of the email"),
		),
	)

	downloadTool := mcp.NewTool("download-audio",
		mcp.WithDescription("Download the audio of a video URL (e.g. from search-youtube) and save it as an audio file via yt-dlp"),
		mcp.WithString("url",
			mcp.Required(),
			mcp.Description("Video URL to download audio from"),
		),
	)

	getDateTimeTool := mcp.NewTool("get-todays-datetime",
		mcp.WithDescription("get today's date and time (local)"),
	)

	getDateTimeUTCTool := mcp.NewTool("get-todays-datetime-UTC",
		mcp.WithDescription("get today's date and time (UTC)"),
	)

	getModelsTool := mcp.NewTool("get-models",
		mcp.WithDescription("List the models served by the local LLM endpoint, plus the one currently configured for summarization"),
	)

	wordDefinitionTool := mcp.NewTool("getworddefinition",
		mcp.WithDescription("Look up a word using the local LLM as a dictionary and thesaurus. Returns entries in the dictionaryapi.dev schema: phonetics, origin, and per-part-of-speech definitions with examples, synonyms, and antonyms"),
		mcp.WithString("word",
			mcp.Required(),
			mcp.Description("The single word to define"),
		),
		mcp.WithString("model",
			mcp.Description("Optional model id for the lookup; defaults to LLM_MODEL"),
		),
	)

	s.AddTool(getModelsTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		result, err := listModels()
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		data, _ := json.Marshal(result)
		return mcp.NewToolResultText(string(data)), nil
	})

	s.AddTool(wordDefinitionTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		word, err := req.RequireString("word")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		result, err := getWordDefinition(word, req.GetString("model", ""))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		data, _ := json.Marshal(result)
		return mcp.NewToolResultText(string(data)), nil
	})

	s.AddTool(googleTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query, err := req.RequireString("query")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		result, err := searchGoogle(query, req.GetString("model", ""))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		data, _ := json.Marshal(result)
		return mcp.NewToolResultText(string(data)), nil
	})

	s.AddTool(bigGoogleTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query, err := req.RequireString("query")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		maxPages := req.GetInt("max_pages", megaDefaultMaxPages)

		result, err := megaGoogleSearch(query, maxPages, req.GetString("model", ""))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		data, _ := json.Marshal(result)
		return mcp.NewToolResultText(string(data)), nil
	})

	s.AddTool(googleSummaryTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query, err := req.RequireString("query")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		maxPages := req.GetInt("max_pages", megaDefaultMaxPages)

		result, err := megaGoogleSearchReduced(query, maxPages, req.GetString("model", ""))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		data, _ := json.Marshal(result)
		return mcp.NewToolResultText(string(data)), nil
	})

	s.AddTool(wikipediaTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query, err := req.RequireString("query")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		result, err := searchWikipedia(query, req.GetString("model", ""))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		data, _ := json.Marshal(result)
		return mcp.NewToolResultText(string(data)), nil
	})

	s.AddTool(youtubeTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query, err := req.RequireString("query")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		maxResults := req.GetInt("max_results", youtubeDefaultMaxResults)

		result, err := searchYouTube(query, maxResults)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		data, _ := json.Marshal(result)
		return mcp.NewToolResultText(string(data)), nil
	})

	s.AddTool(downloadTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		url, err := req.RequireString("url")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		result, err := downloadAudio(url)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		data, _ := json.Marshal(result)
		return mcp.NewToolResultText(string(data)), nil
	})

	s.AddTool(pageGrabTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		url, err := req.RequireString("url")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		result, err := getPage(url, req.GetString("model", ""))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		data, _ := json.Marshal(result)
		return mcp.NewToolResultText(string(data)), nil
	})

	s.AddTool(summarizeNewsDayTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		urls, err := req.RequireStringSlice("newsSources")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		result, err := summarizeNews(urls, 0, req.GetString("model", ""))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		data, _ := json.Marshal(result)
		return mcp.NewToolResultText(string(data)), nil
	})

	s.AddTool(summarizeMySitesTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		rawSites, ok := req.GetArguments()["sites"]
		if !ok {
			return mcp.NewToolResultError("missing required argument 'sites'"), nil
		}
		encoded, _ := json.Marshal(rawSites)
		var sites []SiteRequest
		if err := json.Unmarshal(encoded, &sites); err != nil {
			return mcp.NewToolResultError("invalid 'sites' argument: " + err.Error()), nil
		}

		result, err := summarizeMySitesInstructed(sites, 0, req.GetString("model", ""))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		data, _ := json.Marshal(result)
		return mcp.NewToolResultText(string(data)), nil
	})

	s.AddTool(sendEmailTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		to, err := req.RequireStringSlice("to")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		subject, err := req.RequireString("subject")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		body, err := req.RequireString("body")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		name := req.GetString("name", "")

		result, err := sendEmail(to, name, subject, body)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		data, _ := json.Marshal(result)
		return mcp.NewToolResultText(string(data)), nil
	})

	s.AddTool(getModelsTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		result, err := listModels()
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		data, _ := json.Marshal(result)
		return mcp.NewToolResultText(string(data)), nil
	})

	// Local price tracker: record-price, get-price-history, compare-prices,
	// list-tracked-products. Skipped if the DB can't be opened.
	if store, err := openStore(dbPath()); err != nil {
		log.Printf("price tracker tools disabled: %v", err)
	} else {
		defer store.Close()
		registerPriceTools(s, store)
	}

	s.AddTool(getDateTimeTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		result, err := getDateTime()
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		data, _ := json.Marshal(result)
		return mcp.NewToolResultText(string(data)), nil
	})

	s.AddTool(getDateTimeUTCTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		result, err := getDateTimeUTC()
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		data, _ := json.Marshal(result)
		return mcp.NewToolResultText(string(data)), nil
	})

	server.ServeStdio(s)
}

func handleGoogleSearch(c *gin.Context) {
	query := c.Query("q")
	if query == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing required query parameter 'q'"})
		return
	}

	result, err := searchGoogle(query, c.Query("model"))
	respond(c, result, err)
}

func handleMegaSearch(c *gin.Context) {
	query := c.Query("q")
	if query == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing required query parameter 'q'"})
		return
	}

	maxPages, _ := strconv.Atoi(c.Query("max"))
	result, err := megaGoogleSearch(query, maxPages, c.Query("model"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error(), "result": result})
		return
	}
	c.JSON(http.StatusOK, result)
}

func handleMegaSearchReduced(c *gin.Context) {
	query := c.Query("q")
	if query == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing required query parameter 'q'"})
		return
	}

	maxPages, _ := strconv.Atoi(c.Query("max"))
	result, err := megaGoogleSearchReduced(query, maxPages, c.Query("model"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error(), "result": result})
		return
	}
	c.JSON(http.StatusOK, result)
}

func handleNews(c *gin.Context) {
	var sites []string
	if raw := c.Query("sites"); raw != "" {
		sites = strings.Split(raw, ",")
	}
	maxPages, _ := strconv.Atoi(c.Query("max"))

	result, err := summarizeNews(sites, maxPages, c.Query("model"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error(), "result": result})
		return
	}
	c.JSON(http.StatusOK, result)
}

func handleMySites(c *gin.Context) {
	var sites []string
	if raw := c.Query("sites"); raw != "" {
		sites = strings.Split(raw, ",")
	}
	maxPages, _ := strconv.Atoi(c.Query("max"))

	result, err := summarizeMySites(sites, maxPages, c.Query("model"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error(), "result": result})
		return
	}
	c.JSON(http.StatusOK, result)
}

func handleMySitesInstructed(c *gin.Context) {
	var body struct {
		Sites []SiteRequest `json:"sites"`
		Max   int           `json:"max"`
		Model string        `json:"model"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON body: " + err.Error()})
		return
	}
	if len(body.Sites) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing required field 'sites'"})
		return
	}

	result, err := summarizeMySitesInstructed(body.Sites, body.Max, body.Model)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error(), "result": result})
		return
	}
	c.JSON(http.StatusOK, result)
}

func handleWikipediaSearch(c *gin.Context) {
	query := c.Query("q")
	if query == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing required query parameter 'q'"})
		return
	}

	result, err := searchWikipedia(query, c.Query("model"))
	respond(c, result, err)
}

func handleYouTubeSearch(c *gin.Context) {
	query := c.Query("q")
	if query == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing required query parameter 'q'"})
		return
	}

	maxResults, _ := strconv.Atoi(c.Query("max"))
	result, err := searchYouTube(query, maxResults)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error(), "result": result})
		return
	}
	c.JSON(http.StatusOK, result)
}

func handleDirectPageGrab(c *gin.Context) {
	url := c.Query("url")
	if url == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing required query parameter 'url'"})
		return
	}

	result, err := getPage(url, c.Query("model"))
	respond(c, result, err)
}

func handleGetWordDefinition(c *gin.Context) {
	word := c.Query("word")
	if word == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing required query parameter 'word'"})
		return
	}

	result, err := getWordDefinition(word, c.Query("model"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, result)
}

func handleListModels(c *gin.Context) {
	result, err := listModels()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error(), "result": result})
		return
	}
	c.JSON(http.StatusOK, result)
}

func handleDownloadAudio(c *gin.Context) {
	var body struct {
		URL string `json:"url"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON body: " + err.Error()})
		return
	}
	if body.URL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing required field 'url'"})
		return
	}

	result, err := downloadAudio(body.URL)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error(), "result": result})
		return
	}
	c.JSON(http.StatusOK, result)
}

func handleSendEmail(c *gin.Context) {
	var body struct {
		To      []string `json:"to"`
		Name    string   `json:"name"`
		Subject string   `json:"subject"`
		Body    string   `json:"body"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON body: " + err.Error()})
		return
	}
	if len(body.To) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing required field 'to'"})
		return
	}

	result, err := sendEmail(body.To, body.Name, body.Subject, body.Body)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error(), "result": result})
		return
	}
	c.JSON(http.StatusOK, result)
}

func respond(c *gin.Context, result SearchResult, err error) {
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":  err.Error(),
			"result": result,
		})
		return
	}

	c.JSON(http.StatusOK, result)
}
