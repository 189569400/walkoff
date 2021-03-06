package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	s "strings"
	"syscall"
	"time"

	"./helpers"
	"github.com/go-redis/redis"
)

var cwd, _ = os.Getwd()
var path = filepath.Dir(cwd)
var app_name = os.Getenv("APP_NAME")
var app_version = os.Getenv("APP_VERSION")
var app_group = app_name + ":" + app_version

const UUID_GLOB = "[abcdefABCDEF0123456789][abcdefABCDEF0123456789][abcdefABCDEF0123456789][abcdefABCDEF0123456789][abcdefABCDEF0123456789][abcdefABCDEF0123456789][abcdefABCDEF0123456789][abcdefABCDEF0123456789]-[abcdefABCDEF0123456789][abcdefABCDEF0123456789][abcdefABCDEF0123456789][abcdefABCDEF0123456789]-[abcdefABCDEF0123456789][abcdefABCDEF0123456789][abcdefABCDEF0123456789][abcdefABCDEF0123456789]-[abcdefABCDEF0123456789][abcdefABCDEF0123456789][abcdefABCDEF0123456789][abcdefABCDEF0123456789]-[abcdefABCDEF0123456789][abcdefABCDEF0123456789][abcdefABCDEF0123456789][abcdefABCDEF0123456789][abcdefABCDEF0123456789][abcdefABCDEF0123456789][abcdefABCDEF0123456789][abcdefABCDEF0123456789][abcdefABCDEF0123456789][abcdefABCDEF0123456789][abcdefABCDEF0123456789][abcdefABCDEF0123456789]"
const REDIS_ABORTING_WORKFLOWS = "aborting-workflows"

const hostname = "ANDREWPC"

func check(e error) {
	if e != nil {
		panic(e)
	}
}

func main() {
	fmt.Println("Starting App SDK")
	if app_name == "" {
		panic("set APP_NAME environment variable!")
	}
	if app_version == "" {
		panic("set APP_VERSION environment variable!")
	}
	fmt.Println("App: " + app_name + ", Version: " + app_version)
	fmt.Println("Working Directory: " + cwd)
	fmt.Println("App Path: " + path)
	fmt.Println("---------------------------------------------------------------")
	// API Execution Options
	localPtr := flag.Bool("local", false, "run action locally")
	longPtr := flag.Bool("long", false, "run app_sdk forever")

	// Execute Command Line Parsing
	flag.Parse()

	host := ""
	if *localPtr == true {
		host = "localhost:6379"
	} else {
		host = "resource_redis:6379"
	}

	//Start Redis connection
	client := redis.NewClient(&redis.Options{
		Addr:     host,
		Password: "", // no password set
		DB:       0,  // use default DB
	})

	listen(client, *localPtr, *longPtr)
}

func listen(client *redis.Client, local bool, long bool) {
	streams := []string{}
	old_streams := []string{}

	for {
		time.Sleep(time.Second)
		//Return only new streams
		new_streams := helpers.Diff(get_streams(client), old_streams)

		if len(new_streams) >= 1 {
			streams = helpers.Diff(new_streams, streams)
			old_streams = append(old_streams, streams...)

			//If there is a new stream...
			if len(streams) >= 1 {
				get_actions(client, streams)
			} else if len(streams) < 1 && long == false {
				os.Exit(0)
			}
		}
	}
}

func get_streams(client *redis.Client) []string {
	streams := []string{}

	//Gets the streams
	iter := client.Scan(0, fmt.Sprintf("%s:%s", UUID_GLOB, app_group), 0).Iterator()
	aborted, err := client.SMembers(REDIS_ABORTING_WORKFLOWS).Result()
	check(err)

	// Checks if stream is in the aborted list
	for iter.Next() {
		val := iter.Val()
		if helpers.Index(aborted, s.Split(val, ":")[0]) == -1 {
			streams = append(streams, val)
		}
	}
	if err := iter.Err(); err != nil {
		fmt.Println("Error iterating streams in fn get_streams")
		fmt.Println(err)
	}

	return streams
}

func get_message(client *redis.Client, streams []string) []redis.XStream {
	//format XReadGroupArgs streams
	pendingids := []string{}
	for i := range streams {
		pendingids = append(pendingids, streams[i])
		pendingids = append(pendingids, "0")
	}
	// Check for pending messages
	message, err := client.XReadGroup(&redis.XReadGroupArgs{Group: app_group, Consumer: hostname, Streams: pendingids, Count: 1, Block: (1 * time.Second)}).Result()
	if err != nil || err == redis.Nil {
		fmt.Println("Error getting pending messages in fn get_actions_local")
		fmt.Println(err)
	}
	if len(message) > 0 {
		if len(message[0].Messages) < 1 {

			//format XReadGroupArgs streams
			newids := []string{}
			for i := range streams {
				newids = append(newids, streams[i])
				newids = append(newids, ">")
			}

			message, err = client.XReadGroup(&redis.XReadGroupArgs{Group: app_group, Consumer: hostname, Streams: newids, Count: 1, Block: (1 * time.Second)}).Result()
			if err != nil && err != redis.Nil {
				fmt.Println("Error getting new messages in fn get_actions_local")
				fmt.Println(err)
			}
		}
	}
	return message
}

func get_actions(client *redis.Client, streams []string) {
	var data map[string]interface{}

	result := get_message(client, streams)
	if len(result) >= 1 {
		if len(result[0].Messages) == 1 {
			message := result[0].Messages[0]
			stream := result[0].Stream

			id_ := message.ID
			key := s.Split(stream, ":")[0]
			value := message.Values[key]
			in := []byte(value.(string))
			json.Unmarshal(in, &data)
			fmt.Println(data)
			command, arguments := split_command_string(fmt.Sprintf("%s", data["cmd"]))
			isStdOut, isStdErr, isFile, isDir := check_watchers(data["watchers"].([]interface{}))

			binary := "/app/walkoff_app_sdk/action.exe"
			args := []string{"action.exe",
				//"-local",
				"-stream=" + stream,
				"-message_id=" + id_,
				"-message=" + value.(string),
				"-command=" + command,
				"-arguments=" + arguments}

			if isStdOut {
				args = append(args, "-stdout")
			}
			if isStdErr {
				args = append(args, "-stderr")
			}
			if isFile {
				args = append(args, "-file")
			}
			if isDir {
				args = append(args, "-dir")
			}
			//"-streaming"
			//"-file_watch_path=log_file.log",
			//"-file_delim=",
			//"-dir_watch_path=",

			env := setParameters(data["parameters"].([]interface{}))
			execErr := syscall.Exec(binary, args, env)
			check(execErr)
			os.Exit(1)
		}
	} else {
		//No messages exit
		os.Exit(1)
	}
}

func check_watchers(watchers []interface{}) (bool, bool, bool, bool) {
	isStdOut := false
	isStdErr := false
	isFile := false
	isDir := false
	for w := range watchers {
		watch := watchers[w].(map[string]interface{})
		if watch["name"] == "stdout" {
			isStdOut = true
		} else if watch["name"] == "stderr" {
			isStdErr = true
		} else if watch["name"] == "file" {
			isFile = true
		} else if watch["name"] == "dir" {
			isDir = true
		}
	}
	return isStdOut, isStdErr, isFile, isDir
}

func split_command_string(command string) (string, string) {
	splitArgs := s.SplitN(command, " ", 2)
	return splitArgs[0], splitArgs[1]
}

func setParameters(parameters []interface{}) []string {
	env := os.Environ()
	for p := range parameters {
		param := parameters[p].(map[string]interface{})
		name := s.Replace(fmt.Sprintf("%v", param["name"]), " ", "_", -1)
		value := fmt.Sprintf("%s=%s", name, param["value"])
		env = append(env, value)
	}
	return env
}
