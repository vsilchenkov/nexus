package usecase

import (
	"context"
	"errors"
	"net/url"

	"nexus/internal/domain/ackspec"
	"nexus/internal/platform/logging"
)

// AckPreviewRequest — что проверяем: спека (как в форме узла) и образец
// входящего запроса. Узел не нужен — предпросмотр работает в НЕсохранённой
// форме, как проверка имени CH-таблицы (§64).
type AckPreviewRequest struct {
	Spec        *ackspec.Spec
	SampleBody  []byte
	SampleQuery url.Values
	SamplePath  string
	ContentType string // Content-Type образца; пусто → трактуется как JSON
}

// AckPreviewReport — результат предпросмотра. Неудача рендера — это НЕ ошибка
// вызова: оператор должен увидеть причину и подсказку прямо в форме, а не
// красный алерт «сервер вернул 500».
type AckPreviewReport struct {
	OK          bool   `json:"ok"`
	Status      int    `json:"status,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Body        string `json:"body,omitempty"`
	Reason      string `json:"reason,omitempty"`      // ackspec.Reason
	Placeholder string `json:"placeholder,omitempty"` // подстановка, на которой сорвалось
	Message     string `json:"message,omitempty"`
}

// AckPreviewUsecase — предпросмотр ответа приёма (§83).
type AckPreviewUsecase struct {
	logger logging.Logger
}

func NewAckPreviewUsecase(logger logging.Logger) *AckPreviewUsecase {
	return &AckPreviewUsecase{logger: logger}
}

// previewID/previewNode — значения nexus.* в предпросмотре. Узнаваемые
// заглушки, чтобы оператор не принял их за реальные.
const (
	previewID   = "00000000-0000-0000-0000-000000000000"
	previewNode = "preview"
)

// Run прогоняет шаблон на образце запроса.
//
// Ошибку возвращает только на невалидной спеке (её показывает форма у поля);
// провал рендера приходит внутри отчёта с ok=false.
func (u *AckPreviewUsecase) Run(_ context.Context, req AckPreviewRequest) (*AckPreviewReport, error) {
	if req.Spec == nil {
		return nil, errors.New("ack preview: spec is required")
	}
	spec := *req.Spec
	spec.SetDefaults()
	if err := spec.Validate(); err != nil {
		return nil, err
	}

	tmpl, err := ackspec.Compile(spec.Body, spec.ContentType)
	if err != nil {
		return nil, err
	}

	ct := req.ContentType
	if ct == "" {
		ct = string(ackspec.ContentTypeJSON)
	}
	out, err := tmpl.Render(&ackspec.Ctx{
		Body:        req.SampleBody,
		ContentType: ct,
		Query:       req.SampleQuery,
		PathSuffix:  req.SamplePath,
		ID:          previewID,
		Node:        previewNode,
		Team:        previewNode,
		// Значения времени и queued фиксированы: предпросмотр должен быть
		// воспроизводимым, иначе два прогона одного шаблона дадут разный ответ
		// и оператор решит, что настройка «плавает».
		Queued: false,
	})
	if err != nil {
		return failedPreview(err), nil
	}
	return &AckPreviewReport{
		OK:          true,
		Status:      spec.EffectiveStatus(false),
		ContentType: spec.ContentType.HTTPValue(),
		Body:        string(out),
	}, nil
}

func failedPreview(err error) *AckPreviewReport {
	rep := &AckPreviewReport{OK: false, Message: err.Error()}
	var re *ackspec.RenderError
	if errors.As(err, &re) {
		rep.Reason = string(re.Reason)
		rep.Placeholder = re.Placeholder
	}
	return rep
}
