package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/Perlence/go-wikigenre/Godeps/_workspace/src/github.com/PuerkitoBio/goquery"
	"github.com/Perlence/go-wikigenre/Godeps/_workspace/src/github.com/franela/goreq"
	"github.com/Perlence/go-wikigenre/Godeps/_workspace/src/github.com/shiena/ansicolor"
	"github.com/Perlence/go-wikigenre/Godeps/_workspace/src/github.com/ttacon/chalk"
)

// ErrNoGenres is returned if scraping yields no genres.
var ErrNoGenres = fmt.Errorf("couldn't find any genres")

var colorStderr = ansicolor.NewAnsiColorWriter(os.Stderr)
var logger = log.New(colorStderr, "", log.LstdFlags)

// Log requests to Wikipedia.
var Verbose = false

const verboseUsage = "print URIs of HTTP requests"

func init() {
	flag.BoolVar(&Verbose, "v", false, verboseUsage)

	goreq.SetConnectTimeout(10 * time.Second)
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: go-wikigenre [-h] [-v] "[ARTIST - ]ALBUM"( "[ARTIST - ]ALBUM")*`)
	fmt.Fprintln(os.Stderr, `  -v=false: `+verboseUsage)
	os.Exit(2)
}

// Prevent data races in goreq the hard way.
// Passing cookiejar to goreq.Request will create a new instance of http.Client,
// so Do method won't write into CheckRedirect field of goreq.DefaultClient.
type dummyCookiejar struct{}

func (c dummyCookiejar) SetCookies(u *url.URL, cookies []*http.Cookie) {}
func (c dummyCookiejar) Cookies(u *url.URL) []*http.Cookie {
	return nil
}

func main() {
	flag.Usage = usage
	flag.Parse()
	args := flag.Args()

	var artistAlbums []artistAlbum
	if len(args) > 0 {
		artistAlbums = artistAlbumsFromCLI(args)
	} else {
		var err error
		artistAlbums, err = artistAlbumsFromStdin()
		if err != nil {
			errorln("error reading from stdin: ", err)
			os.Exit(1)
		}
	}

	code := 0
	gs, errs := multipleAlbumGenres(artistAlbums)
	if errs != nil {
		for _, err := range errs {
			errorln(err)
		}
		code = 1
	}
	for _, g := range gs {
		fmt.Println(strings.Join(g, "; "))
	}
	os.Exit(code)
}

func errorln(arg ...interface{}) {
	fmt.Fprint(colorStderr, chalk.Red)
	fmt.Fprint(colorStderr, arg...)
	fmt.Fprint(colorStderr, chalk.Reset, "\n")
}

type artistAlbum struct {
	artist, album, both string
}

func artistAlbumsFromCLI(args []string) []artistAlbum {
	var result []artistAlbum
	for _, arg := range args {
		parts := strings.SplitN(arg, " - ", 2)
		var artist, album string
		switch len(parts) {
		case 1:
			artist, album = "", arg
		case 2:
			artist, album = parts[0], parts[1]
		}
		result = append(result, artistAlbum{artist, album, arg})
	}
	return result
}

// Read foobar2k items from stdin.
func artistAlbumsFromStdin() ([]artistAlbum, error) {
	s := bufio.NewScanner(os.Stdin)
	var lines []string
	for s.Scan() {
		line := s.Text()
		// Look for a zero-length read.
		if len(line) == 0 {
			break
		}
		lines = append(lines, line)
	}
	if err := s.Err(); err != nil {
		return nil, err
	}

	artistAlbums := make([]artistAlbum, len(lines))
	for i, line := range lines {
		artistAlbums[i] = parseFoobar2kItem(line)
	}

	return artistAlbums, nil
}

var reFoobar2kItem = regexp.MustCompile(`(?:(.+) - )?\[(.+?)?(?: CD\d+)?(?: #\d+)?\]`)

func parseFoobar2kItem(item string) artistAlbum {
	matches := reFoobar2kItem.FindStringSubmatch(item)
	if len(matches) == 0 {
		return artistAlbum{}
	}
	both, artist, album := "", matches[1], matches[2]
	if artist == "" {
		both = album
	} else if album == "" {
		both = artist
	} else {
		both = fmt.Sprintf("%s - %s", artist, album)
	}
	return artistAlbum{artist, album, both}
}

func multipleAlbumGenres(as []artistAlbum) ([][]string, []error) {
	var wg sync.WaitGroup
	m := new(sync.Mutex)
	wg.Add(len(as))
	uniqueArtistAlbumMap := make(map[artistAlbum][]string)
	var errs []error
	for _, aa := range as {
		q := aa
		go func() {
			defer func() {
				m.Unlock()
				wg.Done()
				runtime.Gosched()
			}()

			if q == (artistAlbum{}) {
				return
			}

			m.Lock()
			_, ok := uniqueArtistAlbumMap[q]
			if ok {
				// Don't query if query is already in process.
				return
			}
			uniqueArtistAlbumMap[q] = nil
			m.Unlock()

			gs, err := AlbumGenres(q.artist, q.album)
			m.Lock()
			if err != nil {
				errs = append(errs, fmt.Errorf("error finding genres for %s: %s", q.both, err))
			}
			uniqueArtistAlbumMap[q] = gs
		}()
	}
	wg.Wait()

	var result [][]string
	for _, aa := range as {
		result = append(result, uniqueArtistAlbumMap[aa])
	}
	return result, errs
}

// AlbumGenres searches Wikipedia for album page and scrapes genres from it. At
// least one of artist or album must be given.
func AlbumGenres(artist, album string) ([]string, error) {
	for _, variant := range searchVariants(artist, album) {
		gs, err := albumGenres(variant)
		if err != nil {
			return nil, err
		}
		if len(gs) > 0 {
			return gs, nil
		}
	}
	return nil, ErrNoGenres
}

func searchVariants(artist, album string) []string {
	var variants []string
	if artist != "" && album != "" {
		variants = append(variants, fmt.Sprintf("%s (%s album)", album, artist))
	}
	if album != "" {
		variants = append(variants, fmt.Sprintf("%s (album)", album))
		variants = append(variants, album)
	}
	if artist != "" {
		variants = append(variants, artist)
	}
	return variants
}

func albumGenres(query string) ([]string, error) {
	searchResp, err := searchWikipedia(query)
	if err != nil {
		return nil, err
	}
	// Bail if nothing's found.
	if len(searchResp.uris) == 0 {
		return nil, nil
	}

	uri := searchResp.uris[0] // TODO: check other URIs as well
	resp, err := wikipediaPage(uri)
	if resp.Body != nil {
		defer resp.Body.Close()
	}

	doc, err := goquery.NewDocumentFromResponse(resp.Response)
	if err != nil {
		return nil, err
	}
	return scrapeGenres(doc), nil
}

func searchWikipedia(query string) (searchResponse, error) {
	var sr searchResponse

	resp, err := goreq.Request{
		Uri: "https://en.wikipedia.org/w/api.php",
		QueryString: url.Values{
			"action": {"opensearch"},
			"search": {query},
		},
		UserAgent: "Wikigenre",
		CookieJar: dummyCookiejar{},
	}.Do()
	if err != nil {
		return sr, err
	}
	if !isResponseOK(resp) {
		return sr, fmt.Errorf("search on Wikipedia failed, HTTP status %s", resp.Status)
	}
	if resp.Body != nil {
		defer resp.Body.Close()
	}

	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(&sr); err != nil {
		return sr, err
	}
	return sr, nil
}

// isResponseOK returns false if response code is between 400 and 599.
func isResponseOK(r *goreq.Response) bool {
	return !(400 <= r.StatusCode && r.StatusCode < 600)
}

type searchResponse struct {
	query       string
	suggestions []string
	snippets    []string
	uris        []string
}

func (sr *searchResponse) UnmarshalJSON(data []byte) error {
	assertError := func(o interface{}) error {
		return fmt.Errorf("unable to assert %#v", o)
	}

	var jsonResp []interface{}
	err := json.Unmarshal(data, &jsonResp)
	if err != nil {
		return err
	}

	query, ok := jsonResp[0].(string)
	if !ok {
		return assertError(jsonResp[0])
	}
	suggestions, ok := interfaceToStringSlice(jsonResp[1])
	if !ok {
		return assertError(jsonResp[1])
	}
	snippets, ok := interfaceToStringSlice(jsonResp[2])
	if !ok {
		return assertError(jsonResp[2])
	}
	uris, ok := interfaceToStringSlice(jsonResp[3])
	if !ok {
		return assertError(jsonResp[3])
	}

	sr.query = query
	sr.suggestions = suggestions
	sr.snippets = snippets
	sr.uris = uris
	return nil
}

func interfaceToStringSlice(obj interface{}) ([]string, bool) {
	slice, ok := obj.([]interface{})
	if !ok {
		return nil, ok
	}
	result := make([]string, len(slice))
	for i, v := range slice {
		result[i], ok = v.(string)
		if !ok {
			return nil, ok
		}
	}
	return result, true
}

func wikipediaPage(uri string) (*goreq.Response, error) {
	if Verbose {
		logger.Println(uri)
	}
	resp, err := goreq.Request{
		Uri:       uri,
		CookieJar: dummyCookiejar{},
	}.Do()
	if err != nil {
		return nil, err
	}
	if !isResponseOK(resp) {
		return nil, fmt.Errorf("failed to open Wikipedia page %s, HTTP status %s", uri, resp.Status)
	}
	return resp, nil
}

func scrapeGenres(doc *goquery.Document) []string {
	var result []string
	doc.Find("table.haudio td.category a").
		Each(textFromSelection(&result))
	if len(result) > 0 {
		return result
	}
	doc.Find("table.infobox th>a").
		FilterFunction(func(i int, link *goquery.Selection) bool { return link.Text() == "Genre" }).
		Parent().
		Parent().
		Find("td a").
		Each(textFromSelection(&result))
	return result
}

func textFromSelection(result *[]string) func(int, *goquery.Selection) {
	return func(i int, link *goquery.Selection) {
		*result = append(*result, title(link.Text()))
	}
}

// Title upper-cases only the first letter of each word.
func title(s string) string {
	var parts []string
	for _, part := range strings.Split(s, " ") {
		parts = append(parts, strings.ToUpper(part[0:1])+part[1:])
	}
	return strings.Join(parts, " ")
}
