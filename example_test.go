package throne_test

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	throne "github.com/throneproj/Throne-api"
)

func Example() {
	c, err := throne.FromEnv() // THRONE_ADDR, and THRONE_KEY or THRONE_KEY_FILE
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	st, err := c.Status(ctx)
	if err != nil {
		log.Fatal(err)
	}
	if st.State == throne.StateIdle {
		st, err = c.ConnectLast(ctx)
	} else {
		st, err = c.Disconnect(ctx)
	}
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(st.State)
}

// Speed-test the profiles of group 3 whose names match DE or NL, then connect the fastest.
func ExampleClient_RunTest() {
	c, err := throne.FromEnv()
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	job, err := c.RunTest(ctx, throne.TestSpeed, throne.SelectGroup(3, "DE|NL"))
	if err != nil {
		log.Fatal(err)
	}
	best, fastest := -1, int64(0)
	for _, p := range job.Results {
		if p.DownloadBPS > fastest {
			best, fastest = p.ID, p.DownloadBPS
		}
	}
	if best < 0 {
		log.Fatal("no profile passed the speed test")
	}
	if _, err := c.Connect(ctx, best); err != nil {
		log.Fatal(err)
	}
}

// Connect the profile with the lowest latency in the group Throne shows, retesting
// results older than ten minutes.
func ExampleClient_ConnectLowestLatency() {
	c, err := throne.FromEnv()
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	st, err := c.ConnectLowestLatency(ctx, throne.Selection{}, throne.MaxAge(10*time.Minute))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(st.State, st.Profile.Name)
}

func ExampleErrorCode() {
	c, err := throne.FromEnv()
	if err != nil {
		log.Fatal(err)
	}
	_, err = c.Connect(context.Background(), 12)
	switch {
	case err == nil:
	case throne.ErrorCode(err) == throne.CodeBusy:
		fmt.Println("Throne is busy starting or stopping a profile")
	case errors.Is(err, throne.ErrRejected):
		fmt.Println("wrong key")
	default:
		log.Fatal(err)
	}
}

// A Session sends several requests over one connection.
func ExampleClient_Dial() {
	c, err := throne.FromEnv()
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()
	s, err := c.Dial(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer s.Close()

	groups, err := s.Groups(ctx)
	if err != nil {
		log.Fatal(err)
	}
	for _, g := range groups {
		profiles, err := s.Profiles(ctx, throne.SelectGroup(g.ID, ""))
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(g.Name, len(profiles))
	}
}
