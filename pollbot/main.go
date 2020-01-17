package main

import (
	"bytes"
	"database/sql"
	"flag"
	"fmt"
	"os"

	"github.com/keybase/go-keybase-chat-bot/kbchat"
	"github.com/keybase/go-keybase-chat-bot/kbchat/types/chat1"
	"github.com/keybase/managed-bots/base"
	"github.com/keybase/managed-bots/pollbot/pollbot"
	"golang.org/x/sync/errgroup"
)

type Options struct {
	*base.Options
	HTTPPrefix  string
	LoginSecret string
}

func NewOptions() *Options {
	return &Options{
		Options: base.NewOptions(),
	}
}

type BotServer struct {
	*base.Server

	opts Options
	kbc  *kbchat.API
}

func NewBotServer(opts Options) *BotServer {
	return &BotServer{
		Server: base.NewServer(opts.Announcement, opts.AWSOpts),
		opts:   opts,
	}
}

const backs = "```"

func (s *BotServer) makeAdvertisement() kbchat.Advertisement {
	pollExtended := fmt.Sprintf(`Start either a public or an anonymous poll. Public polls are driven by people clicking reactions on the polling message. Anonymous polls offer a link a user can click to register their vote. The polling service will update the results of anonymous polls as they are received without revealing the voter, while also enforcing one vote per person.

	Example:%s
		!poll "Should we move the office to a beach?" "Yes" "No"
		!poll  --anonymous "Where should the next meetup be?" "Miami" "Las Vegas" "Houston"%s`, backs, backs)

	cmds := []chat1.UserBotCommandInput{
		{
			Name:        "poll",
			Description: "Start a poll",
			ExtendedDescription: &chat1.UserBotExtendedDescription{
				Title: `*!poll* [--anonymous] <prompt> <option1> [option2]...
Start a poll`,
				DesktopBody: pollExtended,
				MobileBody:  pollExtended,
			},
		},
	}
	return kbchat.Advertisement{
		Alias: "Polling Service",
		Advertisements: []chat1.AdvertiseCommandAPIParam{
			{
				Typ:      "public",
				Commands: cmds,
			},
		},
	}
}

func (s *BotServer) getLoginSecret() (string, error) {
	if s.opts.LoginSecret != "" {
		return s.opts.LoginSecret, nil
	}
	path := fmt.Sprintf("/keybase/private/%s/login.secret", s.kbc.GetUsername())
	cmd := s.opts.Command("fs", "read", path)
	var out bytes.Buffer
	cmd.Stdout = &out
	s.Debug("Running `keybase fs read` on %q and waiting for it to finish...\n", path)
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return out.String(), nil
}

func (s *BotServer) Go() (err error) {
	if s.kbc, err = s.Start(s.opts.KeybaseLocation, s.opts.Home); err != nil {
		return err
	}
	loginSecret, err := s.getLoginSecret()
	if err != nil {
		s.Debug("failed to get login secret: %s", err)
		return
	}
	sdb, err := sql.Open("mysql", s.opts.DSN)
	if err != nil {
		s.Debug("failed to connect to MySQL: %s", err)
		return err
	}
	db := pollbot.NewDB(sdb)
	if _, err := s.kbc.AdvertiseCommands(s.makeAdvertisement()); err != nil {
		s.Debug("advertise error: %s", err)
		return err
	}
	if err := s.SendAnnouncement(s.opts.Announcement, "I live."); err != nil {
		s.Debug("failed to announce self: %s", err)
		return err
	}

	httpSrv := pollbot.NewHTTPSrv(s.kbc, db, loginSecret)
	handler := pollbot.NewHandler(s.kbc, httpSrv, db, s.opts.HTTPPrefix)
	var eg errgroup.Group
	eg.Go(func() error { return s.Listen(handler) })
	eg.Go(httpSrv.Listen)
	eg.Go(func() error { return s.HandleSignals(httpSrv) })
	if err := eg.Wait(); err != nil {
		s.Debug("wait error: %s", err)
		return err
	}
	return nil
}

func main() {
	rc := mainInner()
	os.Exit(rc)
}

func mainInner() int {
	opts := NewOptions()
	fs := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	fs.StringVar(&opts.HTTPPrefix, "http-prefix", os.Getenv("BOT_HTTP_PREFIX"), "")
	fs.StringVar(&opts.LoginSecret, "login-secret", os.Getenv("BOT_LOGIN_SECRET"), "Login token secret")
	if err := opts.Parse(fs, os.Args); err != nil {
		fmt.Printf("Unable to parse options: %v\n", err)
		return 3
	}
	if len(opts.DSN) == 0 {
		fmt.Printf("must specify a database DSN\n")
		return 3
	}
	bs := NewBotServer(*opts)
	if err := bs.Go(); err != nil {
		fmt.Printf("error running chat loop: %v\n", err)
	}
	return 0
}
