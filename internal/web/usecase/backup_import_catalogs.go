package usecase

import (
	"net/url"
	"strings"

	"nexus/internal/domain"
	"nexus/internal/domain/backup"
)

// remapCatalogs — шаблоны ClickHouse, группы, хосты, справочники заголовков и
// полей, маски логов, реестр инстансов.
//
// Каждая строка проходит ту же доменную проверку, что при создании через
// интерфейс: файл заверен паролем, то есть автором, а не корректностью
// содержимого (файл старой версии с более мягкой проверкой, собранный вручную).
// Маски, хосты и группы — блокирующая проблема: маска, которую Sender не
// скомпилирует, молча пропустит секрет в лог (§95), хост каталога — часть
// защиты исходящих адресов (§23), на группу ссылаются узлы. Заголовки и поля
// узлы ссылаются по имени — такие строки пропускаются с предупреждением.
func (b *importBuilder) remapCatalogs() {
	b.remapTemplates()
	b.remapGroupsAndHosts()
	b.set.Headers = b.newCatalogEntries("headers", b.src.Headers, b.tgt.Headers, validHeader)
	b.set.RequestFields = b.newCatalogEntries("request_fields", b.src.RequestFields, b.tgt.RequestFields, validRequestField)
	b.remapLogMasks()
	b.remapPeers()
}

// remapLogMasks — маски заменяются целиком: у шаблона нет ключа, а
// дописывание к сидам цели продублировало бы стандартные маски источника.
func (b *importBuilder) remapLogMasks() {
	for _, m := range b.src.LogMasks {
		d := domain.LogMaskPattern{
			Pattern: m.Pattern, Replacement: m.Replacement, Description: m.Description, SortOrder: m.SortOrder,
		}
		if err := d.Validate(); err != nil {
			b.block("log_mask_invalid", m.Pattern, err.Error())
		}
	}
	b.set.LogMasks = b.src.LogMasks
	b.preview.Entities["log_masks"] = BackupEntityPlan{Create: len(b.src.LogMasks), Replace: len(b.tgt.LogMasks)}
}

// validGroup — диапазон порядка проверяется до сужения до int32: иначе
// переполнение превратило бы недопустимое значение в допустимое.
func validGroup(g backup.NodeGroup) error {
	if g.SortOrder < 0 || g.SortOrder > domain.MaxNodeGroupSortOrder {
		return domain.ErrNodeGroupSortOrderRange
	}
	return (&domain.NodeGroup{Name: g.Name, Description: g.Description, SortOrder: int32(g.SortOrder)}).Validate()
}

func validHeader(e backup.CatalogEntry) error {
	return (&domain.HeaderCatalogEntry{Name: e.Name, Description: e.Description}).Validate()
}

func validRequestField(e backup.CatalogEntry) error {
	return (&domain.RequestFieldCatalogEntry{Name: e.Name, Description: e.Description}).Validate()
}

// remapTemplates — совпадение по точному имени: строка цели получает описание
// и спецификацию из файла. «По умолчанию» — как в файле.
func (b *importBuilder) remapTemplates() {
	byName := map[string]string{}
	for _, t := range b.tgt.CHTemplates {
		byName[t.Name] = t.ID
	}
	for _, t := range b.src.CHTemplates {
		id, existing := byName[t.Name]
		if !existing {
			id = t.ID
		}
		b.templateID[t.ID] = id
		if t.IsDefault {
			b.set.DefaultTemplateID = id
		}
		t.ID = id
		b.set.CHTemplates = append(b.set.CHTemplates, t)
		if existing {
			b.count("ch_templates", 0, 1)
		} else {
			b.count("ch_templates", 1, 0)
		}
	}
}

func (b *importBuilder) remapGroupsAndHosts() {
	groups := map[string]string{}
	for _, g := range b.tgt.NodeGroups {
		groups[strings.ToLower(g.Name)] = g.ID
	}
	for _, g := range b.src.NodeGroups {
		if id, ok := groups[strings.ToLower(g.Name)]; ok {
			b.groupID[g.ID] = id
			b.count("node_groups", 0, 1)
			continue
		}
		if err := validGroup(g); err != nil {
			b.block("node_group_invalid", g.Name, err.Error())
		}
		b.groupID[g.ID] = g.ID
		b.set.NodeGroups = append(b.set.NodeGroups, g)
		b.count("node_groups", 1, 0)
	}
	hosts := map[string]backup.Host{}
	for _, h := range b.tgt.Hosts {
		hosts[strings.ToLower(h.Pattern)] = h
	}
	for _, h := range b.src.Hosts {
		if cur, ok := hosts[strings.ToLower(h.Pattern)]; ok {
			b.hostByID[h.ID] = cur
			b.count("hosts", 0, 1)
			continue
		}
		d := domain.HostAllowlistEntry{Pattern: h.Pattern, Kind: domain.HostKind(h.Kind), Description: h.Description}
		if err := d.Validate(); err != nil {
			b.block("host_invalid", h.Pattern, err.Error())
		}
		b.hostByID[h.ID] = h
		b.set.Hosts = append(b.set.Hosts, h)
		b.count("hosts", 1, 0)
	}
}

// newCatalogEntries — строки справочника, которых на цели нет. Узлы ссылаются
// на заголовки и поля по имени, поэтому совпавшая строка просто остаётся, а
// не прошедшая проверку пропускается: справочник — подсказка в форме узла,
// сам узел хранит имя у себя.
func (b *importBuilder) newCatalogEntries(entity string, src, tgt []backup.CatalogEntry, valid func(backup.CatalogEntry) error) []backup.CatalogEntry {
	have := map[string]bool{}
	for _, e := range tgt {
		have[strings.ToLower(e.Name)] = true
	}
	var out []backup.CatalogEntry
	for _, e := range src {
		if have[strings.ToLower(e.Name)] {
			b.count(entity, 0, 1)
			continue
		}
		if err := valid(e); err != nil {
			b.warn("catalog_entry_invalid", e.Name, err.Error())
			continue
		}
		out = append(out, e)
		b.count(entity, 1, 0)
	}
	return out
}

// remapPeers — инстанс с адресом, который уже есть в реестре цели, или с
// адресом самой цели (переезд A→B, где B был соседом A) не переносится.
func (b *importBuilder) remapPeers() {
	have := map[string]bool{}
	for _, p := range b.tgt.PeerInstances {
		have[strings.ToLower(p.BaseURL)] = true
	}
	self := selfHosts(b.req.SelfHost, publicBaseURL(b.tgt.AppSettings))
	for _, p := range b.src.PeerInstances {
		switch {
		case have[strings.ToLower(p.BaseURL)]:
			b.skip("peer_instance_exists", p.BaseURL)
			b.count("peer_instances", 0, 1)
		case self[urlHost(p.BaseURL)]:
			b.skip("peer_instance_self", p.BaseURL)
		default:
			b.set.PeerInstances = append(b.set.PeerInstances, p)
			b.count("peer_instances", 1, 0)
		}
	}
}

// selfHosts — адреса самой цели в нижнем регистре: Host запроса и хост
// публичного адреса из настроек.
func selfHosts(requestHost, publicURL string) map[string]bool {
	out := map[string]bool{}
	if h := strings.ToLower(strings.TrimSpace(requestHost)); h != "" {
		out[h] = true
	}
	if h := urlHost(publicURL); h != "" {
		out[h] = true
	}
	return out
}

// urlHost — authority адреса (host[:port]) в нижнем регистре; "" для
// неразбираемого.
func urlHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Host)
}
