package steam

import (
	"encoding/json"
	"fmt"
	"github.com/edwingeng/deque/v2"
	"log"
	"net/http"
	"sync"
	"time"
)

var servers *deque.Deque[string] = nil
var serversRWMutex sync.RWMutex

var httpClient = &http.Client{Timeout: 10 * time.Second}

func InitServers() error {
	servers = deque.NewDeque[string]()
	if err := getServers(); err != nil {
		return err
	}
	return nil
}

func GetServer() string {
	if servers == nil || servers.IsEmpty() {
		log.Fatalf("Empty Servers")
	}

	serversRWMutex.RLock()
	server, _ := servers.Front()
	serversRWMutex.RUnlock()

	return server
}

func BlockServerAndBackNewServer(server string) string {
	serversRWMutex.Lock()
	fmt.Println("Block IP")
	index := -1

	servers.Range(func(i int, v string) bool {
		if v == server {
			index = i
			return true
		}

		return false
	})

	if index != -1 {
		servers.Remove(index)
	}

	serversRWMutex.Unlock()

	return GetServer()
}

func getServers() error {
	for i := 0; i < 15; i++ {
		resp, err := httpClient.Get(fmt.Sprintf("https://api.steampowered.com/ISteamDirectory/GetCMListForConnect/v1/?cellId=%d", i))
		if err != nil {
			return fmt.Errorf("[client] error getting servers: %w", err)
		}

		var data Response
		if err = json.NewDecoder(resp.Body).Decode(&data); err != nil {
			resp.Body.Close()
			return fmt.Errorf("[client] error parse servers: %w", err)
		}
		resp.Body.Close()

		for _, server := range data.Response.ServerList {
			if server.Type != "websockets" {
				continue
			}

			serversRWMutex.Lock()
			servers.PushBack(server.EndPoint)
			serversRWMutex.Unlock()
		}
	}

	if servers.IsEmpty() {
		return fmt.Errorf("[client] Steam returned zero servers")
	}

	return nil
}

type Response struct {
	Response struct {
		ServerList []ServerList `json:"serverlist"`
	} `json:"response"`
}

type ServerList struct {
	EndPoint string `json:"endpoint"`
	Type     string `json:"type"`
}
