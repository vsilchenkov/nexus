package reloader

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// In-package тест разворота SectionAll (§51): забытая в sectionsFor новая
// секция означает, что publish("all") её не применит — фиксируем контрактом.
func TestSectionsFor_Single(t *testing.T) {
	t.Parallel()
	assert.Equal(t, []Section{SectionSentry}, sectionsFor(SectionSentry))
	assert.Equal(t, []Section{SectionLogging}, sectionsFor(SectionLogging))
}

func TestSectionsFor_AllIncludesEveryKnownSection(t *testing.T) {
	t.Parallel()
	got := sectionsFor(SectionAll)
	want := []Section{
		SectionSentry, SectionClickHouse, SectionNotifications,
		SectionSecurity, SectionLogging, SectionMail,
		// §94.5: срок хранения журнала отказов. Без разворота «применить все
		// настройки» не тронул бы ни чистку в Web, ни включение сбора в Receiver.
		SectionGeneral,
	}
	assert.ElementsMatch(t, want, got)
	assert.NotContains(t, got, SectionAll)
}
