package vk

import (
	"html"
	"strings"
	"unicode/utf8"
)

// Лимиты ВКонтакте, которых у телеграма нет: подпись кнопки в сорок символов и
// длина сообщения. Оба выражены в символах, а не байтах.

// maxMessageLen — потолок длины сообщения ВКонтакте.
//
// Лимит там считается в символах, а мы меряем байты: для русского текста в
// UTF-8 это вдвое строже, чем нужно, и ошибиться в опасную сторону не даёт.
const maxMessageLen = 4096

// maxLabelLen — потолок длины подписи кнопки во ВКонтакте.
const maxLabelLen = 40

// cutMark — чем помечаем обрезанное, ellipsis — то же для конца сообщения.
const (
	cutMark  = "…"
	ellipsis = "\n" + cutMark
)

// quoteBar — чем отрисовываем цитату.
const quoteBar = "│ "

// plain переводит телеграмную разметку в то, что ВКонтакте способен показать.
//
// botcore верстает сообщения десятком HTML-тегов телеграма — это осознанный
// выбор общего форматтера на обе платформы. Разметки в сообщениях ВКонтакте
// нет вовсе: ни HTML, ни markdown, ни parse_mode, а юникодного жирного для
// кириллицы не существует. Единственное, чем здесь можно передать структуру, —
// сами символы.
//
// Поэтому теги не выбрасываются, а отрисовываются. Жирному и курсиву в простом
// тексте соответствия нет, и они просто уходят, а вот <blockquote> несёт всю
// структуру: в дне он отделяет подробности пары от её шапки, в неделе — пары
// от названия дня. Без него сообщение превращалось в ровную простыню, где
// заголовок дня ничем не отличался от строки с парой.
func plain(s string) string {
	var b strings.Builder
	b.Grow(len(s) + len(quoteBar)*8)

	quoted, lineStart := false, true

	// emit пишет кусок текста, ставя полоску в начале каждой строки цитаты.
	// Пустые строки не подчёркиваем: полоска в никуда только шумит.
	emit := func(text string) {
		for text != "" {
			if lineStart && quoted && text[0] != '\n' {
				b.WriteString(quoteBar)
			}
			nl := strings.IndexByte(text, '\n')
			if nl < 0 {
				b.WriteString(text)
				lineStart = false
				return
			}
			b.WriteString(text[:nl+1])
			lineStart = true
			text = text[nl+1:]
		}
	}

	for {
		open := strings.IndexByte(s, '<')
		if open < 0 {
			break
		}
		end := strings.IndexByte(s[open:], '>')
		if end < 0 {
			// Одинокая «<» — это не тег, а текст; дальше искать нечего.
			break
		}
		emit(s[:open])

		// Телеграм знает и «<blockquote expandable>», так что сверяем начало.
		switch tag := s[open+1 : open+end]; {
		case strings.HasPrefix(tag, "blockquote"):
			quoted = true
		case tag == "/blockquote":
			quoted = false
		}
		s = s[open+end+1:]
	}
	emit(s)

	// Экранирование снимаем после тегов, а не до: пока «<» из пользовательского
	// текста остаётся «&lt;», его нельзя спутать с началом тега.
	return strings.TrimSpace(html.UnescapeString(b.String()))
}

// label укорачивает подпись кнопки до лимита ВКонтакте.
//
// Названия приходят от вуза и укладываться в сорок символов не обязаны:
// «Институт искусств и социокультурного проектирования» — это 51. Отвергается
// при этом не длинная кнопка, а клавиатура целиком, то есть обзор по
// институтам не открывался вовсе. Режем по границе слова: обрезок должен
// оставаться узнаваемым.
func label(s string) string {
	if labelLen(s) <= maxLabelLen {
		return s
	}

	budget := maxLabelLen - 1 // одна позиция под многоточие
	used, cut, word := 0, 0, 0
	for i, r := range s {
		w := runeLen(r)
		if used+w > budget {
			break
		}
		if r == ' ' {
			word = i
		}
		used += w
		cut = i + utf8.RuneLen(r)
	}
	// Обрубок в половину подписи ничего не подсказывает — тогда уж лучше
	// оборвать посреди слова, но показать больше.
	if word > cut/2 {
		cut = word
	}
	return strings.TrimRight(s[:cut], " ") + cutMark
}

// labelLen меряет подпись так, как её считает ВКонтакте.
//
// Считаем не руны, а единицы UTF-16: значок за пределами базовой плоскости там
// занимает две позиции. Для наших подписей это оценка сверху, и ошибиться она
// даёт только в безопасную сторону.
func labelLen(s string) int {
	n := 0
	for _, r := range s {
		n += runeLen(r)
	}
	return n
}

func runeLen(r rune) int {
	if r > 0xFFFF {
		return 2
	}
	return 1
}

// clamp укорачивает сообщение до лимита ВКонтакте.
//
// Расписание в такую длину укладывается с запасом, но название дисциплины
// приходит от вуза и в принципе может быть любым. Молчаливый отказ отправки —
// худший из возможных исходов, поэтому лучше обрезать по границе строки.
func clamp(s string) string {
	if len(s) <= maxMessageLen {
		return s
	}
	cut := maxMessageLen - len(ellipsis)
	if i := strings.LastIndexByte(s[:cut], '\n'); i > cut/2 {
		cut = i
	} else {
		// Резать в середине руны нельзя: ВКонтакте получит битый UTF-8.
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
	}
	return s[:cut] + ellipsis
}
