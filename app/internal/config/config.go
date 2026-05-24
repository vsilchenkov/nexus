package config

import (
	"bus/app/build"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"

	"github.com/cockroachdb/errors"
	"github.com/creasty/defaults"
	"github.com/go-playground/validator/v10"
	"github.com/joho/godotenv"
	"gopkg.in/yaml.v3"
)

type Config struct {
	build.Option

	Server struct {
		Port string `yaml:"Port" binding:"required"`
	} `yaml:"Server" binding:"required"`

	Authorization struct {
		JWT struct {
			Secret     string `yaml:"Secret"`
			Expiration int    `yaml:"Expiration" binding:"required"`
		} `yaml:"JWT" binding:"required"`
	} `yaml:"Authorization" binding:"required"`

	WebSocket struct {
		Expiration int `yaml:"Expiration" binding:"required"`
		API        struct {
			Cache struct {
				Expiration int `yaml:"Expiration" binding:"required"`
			} `yaml:"Cache" binding:"required"`
		} `yaml:"API" binding:"required"`
	} `yaml:"WebSocket" binding:"required"`

	DataBase struct {
		Type        string `yaml:"Type"`
		Host        string `yaml:"Host"`
		Port        int    `yaml:"Port"`
		DBName      string `yaml:"DBName"`
		Credintials struct {
			UserName string `yaml:"UserName"`
			Password string `yaml:"Password"`
		} `yaml:"Credintials"`
	} `yaml:"DataBase"`

	Redis struct {
		Use         bool   `yaml:"Use"`
		Addr        string `yaml:"Addr"`
		DB          int    `yaml:"DB"`
		Credintials struct {
			UserName string `yaml:"UserName"`
			Password string `yaml:"Password"`
		} `yaml:"Credintials"`
	} `yaml:"Redis"`

	Metrics struct {
		Interval int `yaml:"Interval"`
	} `yaml:"Metrics"`

	Migrations Migrations
	Terminal   Terminal

	Log struct {
		Debug        bool   `yaml:"Debug" binding:"required"`
		Level        int    `yaml:"Level" binding:"required"`
		Dir          string `yaml:"Dir" binding:"required"`
		OutputInFile bool   `yaml:"OutputInFile" binding:"required"`
	} `yaml:"Log" binding:"required"`

	Sentry struct {
		Use              bool    `yaml:"Use"`
		Dsn              string  `yaml:"Dsn"`
		Environment      string  `yaml:"Environment"`
		AttachStacktrace bool    `yaml:"AttachStacktrace"`
		TracesSampleRate float64 `yaml:"TracesSampleRate"`
		EnableTracing    bool    `yaml:"EnableTracing"`
		Debug            bool    `yaml:"Debug"`
	} `yaml:"Sentry"`
}

type Migrations struct {
	Up   bool
	Down bool
}

type Terminal struct {
	ChangeUserPassword bool
}

type flags struct {
	configPath string
	logdir     string
	Migrations Migrations
	Terminal   Terminal
}

func New(b build.Option) *Config {
	return &Config{
		Option: b,
	}
}

func ParseFlags() flags {

	var debug bool
	flag.BoolVar(&debug, "debug", false, "Use debug")

	var configPath string
	flag.StringVar(&configPath, "config", "config/config.yml", "Путь к файлу настроек")

	var logdir string
	flag.StringVar(&logdir, "logdir", "", "Путь к каталогу логирования")

	var install bool
	flag.BoolVar(&install, "install", false, "Запуск миграций")

	var uninstall bool
	flag.BoolVar(&uninstall, "uninstall", false, "Отмена миграций")

	var changeUserPassword bool
	flag.BoolVar(&changeUserPassword, "ChangeUserPassword", false, "Изменение пароля пользователя")

	flag.Parse()

	flags := flags{
		configPath: configPath,
		logdir:     logdir,
		Migrations: Migrations{
			Up:   install,
			Down: uninstall,
		},
		Terminal: Terminal{
			ChangeUserPassword: changeUserPassword,
		},
	}

	return flags
}

func LoadSettigs(flags flags, c *Config) error {

	const op = "config.LoadSettigs"

	path := flags.configPath
	fullPath := filepath.Join(c.WorkingDir, path)

	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		return errors.WithMessagef(err, "%s - файл настроек %q не найден", op, fullPath)
	}
	file, err := os.ReadFile(fullPath)
	if err != nil {
		return errors.WithMessagef(err, "%s - ошибка чтения файла %q", op, fullPath)
	}

	if err := yaml.Unmarshal(file, &c); err != nil {
		return errors.WithMessagef(err, "%s - ошибка десириализации настроек", op)
	}

	validate := validator.New()
	err = validate.Struct(c)
	if err != nil {
		return errors.WithMessagef(err, "%s - ошибка валидации настроек", op)
	}

	if err := defaults.Set(c); err != nil {
		return errors.WithMessagef(err, "%s - ошибка установки настроек по умолчанию", op)
	}

	if flags.logdir != "" {
		c.Log.Dir = flags.logdir
	}

	c.Migrations.Up = flags.Migrations.Up
	c.Migrations.Down = flags.Migrations.Down

	c.Terminal.ChangeUserPassword = flags.Terminal.ChangeUserPassword

	loadEnv(c)
	c.DataBase.Credintials.Password = url.QueryEscape(c.DataBase.Credintials.Password)

	fmt.Printf("Settings loaded: %s\n", fullPath)

	return nil
}

func loadEnv(c *Config) {

	fullPath := filepath.Join(c.WorkingDir, ".env")
	godotenv.Load(fullPath)

	loadEnvValue("SENTRY_DSN", &c.Sentry.Dsn)

	loadEnvValue("JWT_SECRET", &c.Authorization.JWT.Secret)

	loadEnvValue("REDIS_ADDR", &c.Redis.Addr)
	loadEnvValue("REDIS_USR", &c.Redis.Credintials.UserName)
	loadEnvValue("REDIS_PWD", &c.Redis.Credintials.Password)

	loadEnvValue("DB_HOST", &c.DataBase.Host)
	loadEnvValue("DB_USR", &c.DataBase.Credintials.UserName)
	loadEnvValue("DB_PWD", &c.DataBase.Credintials.Password)

	port, _ := strconv.Atoi(os.Getenv("DB_PORT"))
	if port != 0 {
		c.DataBase.Port = port
	}

}

func loadEnvValue(key string, field *string) {
	value := os.Getenv(key)
	if value != "" {
		*field = value
	}
}

func (c Config) UseDebug() bool {
	return c.Log.Debug
}

func (t Terminal) Run() bool {
	return t.ChangeUserPassword
}
