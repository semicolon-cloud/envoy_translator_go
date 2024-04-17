package main

import (
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

type Listener struct {
	uuid          string
	listener_name string
	description   string
	ip            string
	external_ip   string
	port          int
	stype         string
	created_on    *time.Time
}
type Route struct {
	uuid           string
	listener       string
	domain_names   []string
	keystone_user  string
	project_id     string
	target_servers []ServerTarget
	created_on     *time.Time
	updated_on     *time.Time
}

type ServerTarget struct {
	Ip   string      `json:"ip"`
	Port json.Number `json:"port"`
}

// "user:password@/dbname"
func DownloadListeners(url string) ([]Listener, []Route, error) {
	db, err := sql.Open("mysql", url)
	if err != nil {
		panic(err)
	}
	db.SetConnMaxLifetime(time.Minute * 3)
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(10)

	var listeners []Listener

	rows, err := db.Query("SELECT * from listeners_listener")
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var l Listener
		if err := rows.Scan(&l.uuid, &l.listener_name, &l.description, &l.ip, &l.external_ip, &l.port, &l.stype, &l.created_on); err != nil {
			return nil, nil, err
		}
		listeners = append(listeners, l)
	}

	var routes []Route

	rows2, err := db.Query("SELECT * from listeners_route")
	if err != nil {
		return nil, nil, err
	}
	defer rows2.Close()

	for rows2.Next() {
		var r Route
		var domain_names string
		var target_servers string
		if err := rows2.Scan(&r.uuid, &domain_names, &r.keystone_user, &r.project_id, &target_servers, &r.listener, &r.created_on, &r.updated_on); err != nil {
			return nil, nil, err
		}

		r.domain_names = strings.Split(domain_names, ",")
		err := json.Unmarshal([]byte(target_servers), &r.target_servers)
		if err != nil {
			panic(err)
		}
		routes = append(routes, r)
	}

	return listeners, routes, nil
}
