package config

import (
	"flag"
	"fmt"
	"os"
)

// Flags — флаги командной строки, общие для всех трёх бинарей.
type Flags struct {
	ConfigPath       string
	Debug            bool
	ShowVersion      bool
	MigrateUp        bool
	MigrateDownN     int
	MigrateStatus    bool
	SetAdminPassword string // если задано — задаёт пароль admin'у и выходит
}

// ParseFlags парсит argv. Неизвестные флаги — error.
// При флаге --version печатает версию и завершает процесс.
func ParseFlags(version string) Flags {
	fs := flag.NewFlagSet(os.Args[0], flag.ExitOnError)

	var f Flags
	fs.StringVar(&f.ConfigPath, "config", "", "путь к YAML-конфигу (приоритет: флаг > $DATABUS_CONFIG > ./config/config.yml)")
	fs.StringVar(&f.ConfigPath, "c", "", "alias for --config")
	fs.BoolVar(&f.Debug, "debug", false, "загрузить config_debug.yml вместо config.yml")
	fs.BoolVar(&f.ShowVersion, "version", false, "напечатать версию и выйти")
	fs.BoolVar(&f.MigrateUp, "migrate-up", false, "применить все непримененные миграции и выйти")
	fs.IntVar(&f.MigrateDownN, "migrate-down", 0, "откатить N последних миграций и выйти")
	fs.BoolVar(&f.MigrateStatus, "migrate-status", false, "показать текущую версию схемы и выйти")
	fs.StringVar(&f.SetAdminPassword, "set-admin-password", "", "задать пароль admin'у (bootstrap) и выйти")

	_ = fs.Parse(os.Args[1:])

	if f.ShowVersion {
		fmt.Println(version)
		os.Exit(0)
	}
	return f
}
