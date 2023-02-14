package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/fatih/color"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/samber/lo"
)

func main() {
	//ctx, cancel := context.WithTimeout(context.Background(), time.Second*60)
	endpoints, err := ScanSite(context.TODO(), "http://localhost:3000/overview", "admin", "123456")
	//cancel()
	if err != nil {
		log.Println(err)
	}

	log.Println(fmt.Sprintf("total %v endpoints", len(endpoints)))
	for _, endpoint := range endpoints {
		fmt.Printf("url: %v\n", endpoint.URI)
	}
}

func ScanSite(ctx context.Context, url string, username string, password string) ([]Endpoint, error) {
	browser := rod.New().SlowMotion(time.Second).NoDefaultDevice().MustConnect()
	var endpoints []Endpoint

	page := browser.MustPage(url)
	//page.MustWindowFullscreen()

	router := browser.HijackRequests()
	router.MustAdd("*", func(ctx *rod.Hijack) {
		err := ctx.LoadResponse(http.DefaultClient, true)
		if err != nil {
			log.Println(Panic(err))
			return
		}

		// fmt.Printf("url: %v\n", ctx.Request.URL().RequestURI())

		endpoint := Endpoint{
			URI: ctx.Request.URL().RequestURI(),
			Request: Request{
				Body:   ctx.Request.Body(),
				Header: ctx.Request.Headers(),
			},
			Response: Response{
				Code:   ctx.Response.Payload().ResponseCode,
				Body:   ctx.Response.Body(),
				Header: ctx.Response.Headers(),
			},
		}

		endpoints = append(endpoints, endpoint)
	})

	go router.Run()

	time.Sleep(time.Second * 3)
	_ = FillCredentials(page, username, password)
	time.Sleep(time.Second * 3)

	// page.HijackRequests()

	ch := make(chan bool, 1)
	go Traverse(browser, url, ch, page)
	//time.Sleep(time.Hour)
	select {
	case <-ch:
		return uniqify(endpoints), nil
	case <-ctx.Done():
		return uniqify(endpoints), nil
	}
}

func uniqify(endpoints []Endpoint) []Endpoint {
	return lo.UniqBy(endpoints, func(endpoint Endpoint) string {
		return endpoint.URI + "@" + endpoint.Request.Body
	})
}

type Location struct {
	Hops []string
	Text string
	Path string
}

func (location *Location) Key() string {
	return fmt.Sprintf("%s.%s", location.Text, strings.Join(location.Hops, " -> "))
}

var (
	Info  = color.New(color.FgBlack).SprintFunc()
	Succ  = color.New(color.FgGreen).SprintFunc()
	Warn  = color.New(color.FgYellow).SprintFunc()
	Panic = color.New(color.FgRed).SprintFunc()
)

func Traverse(browser *rod.Browser, url string, ch chan bool, page *rod.Page) {
	cache := map[string]bool{}

	locations := make(chan Location, 1000)

	path := GetPath(page)

	elements := findClickable(page)

	for _, element := range elements {
		location := Location{
			Hops: []string{element.MustGetXPath(true)},
			Text: element.MustText(),
			Path: path,
		}
		locations <- location
		cache[location.Key()] = true
	}

	page.MustClose()

	sema := make(chan bool, 5)

	for {
		var location = <-locations

		go func() {
			sema <- true
			page := browser.MustPage(url)
			// page.MustWindowFullscreen()
			defer func() {
				<-sema
				page.MustClose()
			}()

			log.Println(fmt.Sprintf("analysis %s: %s", location.Path, strings.ReplaceAll(location.Text, "\n", " ")))

			page.MustEval(`(href) => window.location.href = href`, path)

			for _, hop := range location.Hops {
				page.MustElementX(hop).MustEval(`
					() => {
						this.dispatchEvent(new MouseEvent('mouseover', { 'bubbles': true }))
						this.click && this.click();
					}
				`)
				time.Sleep(time.Second)
			}

			cache[location.Key()] = true

			elements := findClickable(page)

			currentPath := page.MustEval(`() => window.location.href`).String()

			//progress := pad.Left(fmt.Sprintf("%.2f", 100-Progress(len(locations), len(cache))), 5, " ")
			//log.Println(fmt.Sprintf("%s: %s", path, location.XPath))
			//log.Println(fmt.Sprintf("found %v points, progress: %v%%", len(elements), progress))

			for _, element := range elements {
				location := Location{
					Hops: location.Hops,
					Text: element.MustText(),
				}
				if cache[location.Key()] {
					continue
				}
				location = Location{
					Hops: append(location.Hops, element.MustGetXPath(true)),
					Text: element.MustText(),
					Path: currentPath,
				}
				locations <- location
			}
		}()
		if len(locations) == 0 && len(sema) == 0 {
			break
		}
	}

	ch <- true
}

func Progress(current int, total int) float64 {
	return math.Round(float64(current)*10000.0/float64(total)) / 100
}

func printable(element *rod.Element) string {
	text := strings.TrimSpace(strings.ReplaceAll(element.MustText(), "\n", " "))
	if lo.IsNotEmpty(text) {
		return text
	}
	id := element.MustProperty("id").String()
	if lo.IsNotEmpty(id) {
		return id
	}
	return element.MustGetXPath(true)
}

func GetPath(page *rod.Page) string {
	return page.MustEval(`() => window.location.href`).String()
}

func findClickable(page *rod.Page) rod.Elements {
	elements := page.MustElements("*")
	//elements = lo.UniqBy(elements, func(el *rod.Element) string {
	//	return fmt.Sprintf("%v.%v.%v", el.Object.ClassName, el.MustProperty("class"), el.MustProperty("style"))
	//})
	elements = lo.Filter(elements, func(el *rod.Element, index int) bool {
		return Clickable(el)
	})
	elements = lo.Filter(elements, func(x *rod.Element, _ int) bool {
		return !lo.SomeBy(elements, func(y *rod.Element) bool {
			return x != y && x.MustContainsElement(y)
		})
	})
	// texts := lo.Map(elements, func(item *rod.Element, _ int) string {
	// 	return printable(item)
	// })
	// Inspect(texts)
	return elements
}

func Clickable(element *rod.Element) bool {
	name := element.Object.ClassName
	tags := []string{"HTMLButtonElement", "HTMLAElement"}
	onclick := element.MustEval("() => this.onclick != null").Bool()
	pointer := element.MustEval(`() => getComputedStyle(this).cursor === 'pointer'`).Bool()
	rect := element.MustEval(`
		() => {
			let bodyRect = document.body.getBoundingClientRect();
		 	let rect = this.getBoundingClientRect();
			let item = {
				left: Math.max(rect.left - bodyRect.x, 0),
				top: Math.max(rect.top - bodyRect.y, 0),
				right: Math.min(rect.right - bodyRect.x, document.body.clientWidth),
				bottom: Math.min(rect.bottom - bodyRect.y, document.body.clientHeight)
			}
	 		return (item.right - item.left) * (item.bottom - item.top) >= 20
		}
	`).Bool()
	//return name != "SVGPathElement" && name != "SVGSVGElement" && (lo.Contains(tags, name) || onclick || pointer) && rect
	return !strings.HasPrefix(name, "SVG") && (lo.Contains(tags, name) || onclick || pointer) && rect
}

func FillCredentials(page *rod.Page, username string, password string) bool {
	elements := page.MustElements("*")
	//texts := lo.Map(elements, func(item *rod.Element, index int) string {
	//	return item.MustText()
	//})
	usernameField, found := lo.Find(elements, func(item *rod.Element) bool {
		return maybeUsername(item)
	})

	if found {
		usernameField.MustInput(username)
	} else {
		return false
	}

	submitForm(elements)

	time.Sleep(time.Second)

	elements = page.MustElements("*")

	passwordField, found := lo.Find(elements, func(item *rod.Element) bool {
		_type, _ := item.Attribute("type")
		return item.Object.ClassName == "HTMLInputElement" && (_type != nil && *_type == "password")
	})

	if found {
		passwordField.MustInput(password)
	} else {
		return false
	}

	submitForm(elements)
	return true
}

func submitForm(elements rod.Elements) bool {
	submitField, found := lo.Find(elements, func(item *rod.Element) bool {
		_type, _ := item.Attribute("type")
		return (item.Object.ClassName == "HTMLButtonElement" || item.Object.ClassName == "HTMLInputElement") && (_type != nil && *_type == "submit")
	})

	if !found {
		return false
	}
	submitField.MustEval("() => this.click()")
	return true
}

func maybeUsername(elem *rod.Element) bool {
	_type, _ := elem.Attribute("type")
	placeholder, _ := elem.Attribute("placeholder")
	id, _ := elem.Attribute("id")
	name, _ := elem.Attribute("name")
	autocomplete, _ := elem.Attribute("autocomplete")
	keywords := []string{"user", "user_name", "username", "account", "loginfmt", "用户", "用户名", "账号", "账户", "email", "mail", "邮箱", "login"}
	return elem.Object.ClassName == "HTMLInputElement" && (_type != nil && (*_type == "text" || *_type == "email")) && (id != nil && ContainsMultiKeywordsIgnoreCase(*id, keywords) ||
		autocomplete != nil && ContainsMultiKeywordsIgnoreCase(*autocomplete, keywords) ||
		name != nil && ContainsMultiKeywordsIgnoreCase(*name, keywords) ||
		placeholder != nil && ContainsMultiKeywordsIgnoreCase(*placeholder, keywords))
}

func ContainsMultiKeywordsIgnoreCase(target string, keywords []string) bool {
	return lo.SomeBy(keywords, func(keyword string) bool {
		return strings.Contains(strings.ToLower(target), strings.ToLower(keyword))
	})
}

type Request struct {
	Body   string
	Header proto.NetworkHeaders
}
type Response struct {
	Code   int
	Body   string
	Header http.Header
}
type Endpoint struct {
	URI      string
	Request  Request
	Response Response
}

func Inspect(v any) {
	//data, _ := json.MarshalIndent(v, "", "  ")
	data, _ := json.Marshal(v)
	fmt.Printf("%v\n", string(data))
}
