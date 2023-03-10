package server

import (
	"bufio"
	"errors"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/KavetiRohith/go-cache/cache"
	"github.com/KavetiRohith/go-cache/server/iomultiplexer"
	syscall "golang.org/x/sys/unix"
)

type ServerOpts struct {
	Host             string
	Port             int
	CronFrequency    time.Duration
	lastCronExecTime time.Time
}

type Server struct {
	ServerOpts
	cache       *cache.Cache
	con_clients uint
}

func NewServer(opts ServerOpts, c *cache.Cache) *Server {
	return &Server{
		ServerOpts: opts,
		cache:      c,
	}
}

func (s *Server) Start() error {
	log.Println("starting an asynchronous TCP server on", s.Host, s.Port)

	maxClients := 20000

	// Create a socket
	serverFD, err := syscall.Socket(syscall.AF_INET, syscall.O_NONBLOCK|syscall.SOCK_STREAM, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(serverFD)

	// Set the Socket operate in a non-blocking mode
	err = syscall.SetNonblock(serverFD, true)
	if err != nil {
		return err
	}

	// Bind the IP and the port
	ip4 := net.ParseIP(s.Host)
	err = syscall.Bind(serverFD, &syscall.SockaddrInet4{
		Port: s.Port,
		Addr: [4]byte{ip4[0], ip4[1], ip4[2], ip4[3]},
	})
	if err != nil {
		return err
	}

	// Start listening
	err = syscall.Listen(serverFD, maxClients)
	if err != nil {
		return err
	}

	// AsyncIO starts here!!

	// creating multiplexer instance
	multiplexer, err := iomultiplexer.New(maxClients)
	if err != nil {
		log.Fatal(err)
	}
	defer multiplexer.Close()

	// Listen to read events on the Server itself
	err = multiplexer.Subscribe(iomultiplexer.Event{
		Fd: serverFD,
		Op: iomultiplexer.OP_READ,
	})
	if err != nil {
		return err
	}

	for {
		if time.Now().After(s.lastCronExecTime.Add(s.CronFrequency)) {
			s.cache.DeleteExpiredKeys()
			s.lastCronExecTime = time.Now()
		}

		// poll for events that are ready for IO
		events, err := multiplexer.Poll(-1)
		if err != nil {
			continue
		}

		for _, event := range events {
			// if the socket server itself is ready for an IO
			if event.Fd == serverFD {
				// accept the incoming connection from a client
				fd, _, err := syscall.Accept(serverFD)
				if err != nil {
					log.Println("err", err)
					continue
				}

				// increase the number of concurrent clients count
				s.con_clients++
				syscall.SetNonblock(fd, true)

				// add this new TCP connection to be monitored
				if err := multiplexer.Subscribe(iomultiplexer.Event{
					Fd: fd,
					Op: iomultiplexer.OP_READ,
				}); err != nil {
					return err
				}

			} else {
				conn := fDconn{Fd: int(event.Fd)}

				r := bufio.NewReader(conn)
				cmd, err := r.ReadBytes('\n')

				if err != nil {
					conn.Close()
					s.con_clients--
					continue
				}

				resp, err := s.handlecommand(cmd)
				if err != nil {
					resp = []byte(err.Error())
				}

				_, err = conn.Write(append(resp, '\n'))
				if err != nil {
					conn.Close()
					s.con_clients--
					continue
				}
			}
		}
	}
}

func (s *Server) handlecommand(rawCmd []byte) ([]byte, error) {
	var (
		parts   = strings.Fields(string(rawCmd))
		len_cmd = len(parts)
	)

	if len_cmd < 2 {
		return nil, errors.New("message must atleast have command and key")
	}

	var (
		cmd = parts[0]
		key = parts[1]
	)

	switch cmd {
	case "SET":
		switch len_cmd {
		case 3:
			val := parts[2]
			return s.handleSet(key, val)
		case 4:
			val := parts[2]
			ttl := parts[3]
			return s.handleSetWithTTL(key, val, ttl)
		default:
			return nil, errors.New("SET message must atleast have key and value")
		}
	case "GET":
		return s.handleGet(key)
	case "DEL":
		return s.handleDel(key)
	case "HAS":
		return s.handleHas(key)
	default:
		return nil, fmt.Errorf("unknown Command %s", cmd)
	}
}

func (s *Server) handleSet(key string, val string) ([]byte, error) {
	err := s.cache.Set(key, val)
	if err != nil {
		return nil, err
	}

	log.Printf("SET %s %s\n", key, val)
	return []byte("Success"), nil
}

func (s *Server) handleSetWithTTL(key string, val string, ttl string) ([]byte, error) {
	parsedTTL, err := strconv.Atoi(ttl)
	if err != nil {
		return nil, errors.New("invalid TTl")
	}
	err = s.cache.SetWithTTL(key, val, int64(parsedTTL))
	if err != nil {
		return nil, err
	}

	log.Printf("SET %s %s exp: %v seconds\n", key, val, parsedTTL)
	return []byte("Success"), nil
}

func (s *Server) handleGet(key string) ([]byte, error) {
	val, err := s.cache.Get(key)
	if err != nil {
		return nil, err
	}

	log.Printf("GET %s %s\n", key, val)
	return []byte(val), nil
}

func (s *Server) handleDel(key string) ([]byte, error) {
	err := s.cache.Delete(key)
	if err != nil {
		return nil, err
	}

	log.Printf("DEL %s\n", key)
	return []byte("Success"), nil
}

func (s *Server) handleHas(key string) ([]byte, error) {
	isPresent := s.cache.Has(key)
	log.Printf("HAS %s %v\n", key, isPresent)
	if !isPresent {
		return []byte("No"), nil
	}

	return []byte("Yes"), nil
}
