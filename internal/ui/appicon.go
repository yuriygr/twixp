//go:build windows
// +build windows

package ui

import "github.com/lxn/walk"

// appIconResourceID — ID иконки в rsrc.syso. Не наугад: Makefile
// (таргет rsrc) зовёт rsrc так — "-ico .assets/app.ico -manifest
// .assets/manifest.xml" — а сама rsrc присваивает ID общим счётчиком в
// порядке обработки, и манифест у неё обрабатывается ПЕРВЫМ независимо
// от порядка флагов в командной строке (см. исходники akavel/rsrc,
// rsrc.go: Embed() сначала берёт newid() для манифеста, потом для
// каждой иконки) — манифесту достаётся 1, иконке — 2. Если в Makefile
// когда-нибудь уберётся -manifest или добавится вторая иконка — это
// число тоже придётся поправить.
const appIconResourceID = 2

// appIcon — иконка приложения из rsrc.syso, общая для главного окна и
// диалогов. Ошибку сознательно проглатываем — так же вело себя каждое
// из трёх старых мест по отдельности: без иконки окно всё равно
// откроется, просто с иконкой по умолчанию, а это не та причина, из-за
// которой стоит останавливать открытие окна.
func appIcon() *walk.Icon {
	icon, _ := walk.NewIconFromResourceId(appIconResourceID)
	return icon
}
