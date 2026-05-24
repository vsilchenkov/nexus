package service

import "bus/app/internal/models"

type Services struct {
	models.Gate
	models.WS
	models.WSApi
}

func New(gate models.Gate, ws models.WS, wsApi models.WSApi) models.Services {
	return &Services{
		Gate:  gate,
		WS:    ws,
		WSApi: wsApi,
	}
}
