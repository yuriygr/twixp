//go:build windows
// +build windows

// Страница входа. Показывается вместо страницы чата (см. pagehost.go),
// когда тихий вход по сохранённому токену не удался — см.
// MainWindow.EnsureSignedIn.
//
// Раньше это был модальный walk.Dialog поверх MainWindow. Отказались от
// диалога: на реальном железе (Eee PC, экран 800×480) MainWindow может
// открыться сдвинутым за пределы экрана (Windows сама выбирает позицию
// через CW_USEDEFAULT, ничего не гарантируя на экране без запаса) —
// строкой состояния и частью правого края. Сам walk.Dialog в этом
// случае не потерялся бы (Dialog.Show вызывает fitRectToScreen и
// прижимает диалог к видимой рабочей области монитора, см. vendor
// lxn/walk, dialog.go) — но будучи МОДАЛЬНЫМ, он на всё время входа
// блокирует ввод в MainWindow, а значит и не даёт пользователю
// перетащить съехавшее окно на место, пока сам диалог открыт. Обычная
// страница внутри уже видимого (и который в принципе можно перетащить
// в любой момент) MainWindow этой проблемы не создаёт вовсе.
package ui

import (
	"fmt"
	"log"

	"github.com/lxn/walk"
	"github.com/lxn/walk/declarative"
)

// blankDeviceCodeText — заготовка на месте строк device-кода, пока
// реального кода ещё нет.
const blankDeviceCodeText = ""

// deviceCodeLineSize — гарантированный минимальный размер одной строки
// device-кода (см. MinSize у deviceCodeLine ниже). Раньше пытались
// резервировать место текстом-плейсхолдером — не сработало ни по
// высоте (calculateTextSize, см. vendor lxn/walk, window.go, обрезает
// пробелы в конце каждой строки через TrimRight, и GetTextExtentPoint32
// на получившейся ПУСТОЙ строке возвращает {0, 0} по ОБЕИМ осям), ни по
// ширине (с пустым текстом измеренная ширина тоже 0, и оба HSpacer по
// бокам съедают всё место, ничего не оставляя лейблу) — причём после
// SetText реальным текстом это не пересчитывается заново, несмотря на
// updateParentLayout внутри Label.SetText. MinSize по обеим осям не
// зависит от текста вообще — гарантирует размер независимо от того,
// что в лейбле сейчас написано, пустая строка или реальный код.
var deviceCodeLineSize = declarative.Size{Width: 460, Height: 26}

// signInPage — контроллер страницы входа. Не знает про MainWindow
// вообще: единственное, что ему нужно от внешнего мира — функция входа
// и коллбэк onSuccess, вызываемый уже в UI-потоке после успешного
// входа. За экран (Synchronize, MsgBox) отвечает root.Form() — тот
// самый MainWindow, но получаемый через обычный API виджета, а не
// явную ссылку.
type signInPage struct {
	root      *walk.Composite
	signInBtn *walk.PushButton

	// Три отдельных однострочных лейбла вместо одного многострочного.
	// Многострочный вариант (текст с \r\n) в этой сборке walk рисует
	// вместо переноса строки "тофу"-глиф, и весь текст сжимается в одну
	// обрезанную по ширине строку — эмпирически подтверждено (см.
	// git-историю), причём воспроизводится даже со статическим Text
	// при создании, без единого SetText, так что дело не в SetText, а
	// в самом рендере многострочного STATIC control. Три независимых
	// однострочных лейбла этот вопрос просто обходят — однострочный
	// Label уже доказанно работает (тот же "Войдите через аккаунт
	// Twitch" выше).
	deviceCodeLine1 *walk.Label // "Откройте"
	deviceCodeLine2 *walk.Label // сам URL (verificationURI)
	deviceCodeLine3 *walk.Label // "и введите код: XXXXXXXX"

	signIn    SignIn
	onSuccess func(SignInResult)
}

// deviceCodeLine строит один центрированный ряд для строки device-кода
// — вынесено в функцию, потому что таких рядов теперь три подряд,
// одинаковых по структуре (см. deviceCodeLine1/2/3 выше).
func deviceCodeLine(assignTo **walk.Label) declarative.Widget {
	return declarative.Composite{
		Layout: declarative.HBox{MarginsZero: true},
		Children: []declarative.Widget{
			declarative.HSpacer{},
			declarative.Label{
				AssignTo: assignTo,
				Text:     blankDeviceCodeText,
				MinSize:  deviceCodeLineSize,
				Font:     declarative.Font{PointSize: 14, Bold: true},
			},
			declarative.HSpacer{},
		},
	}
}

// buildSignInPage — pageFactory для pageHost.show (см. pagehost.go).
func buildSignInPage(signIn SignIn, onSuccess func(SignInResult)) pageFactory {
	return func(parent walk.Container) (*walk.Composite, error) {
		p := &signInPage{signIn: signIn, onSuccess: onSuccess}
		if err := p.build(parent); err != nil {
			return nil, err
		}
		return p.root, nil
	}
}

func (p *signInPage) build(parent walk.Container) error {
	return (declarative.Composite{
		AssignTo: &p.root,
		Layout:   declarative.VBox{},
		Children: []declarative.Widget{
			declarative.VSpacer{},
			declarative.Composite{
				// Отдельный Composite с HBox + HSpacer'ами по бокам —
				// самый простой способ в этой версии walk отцентровать
				// содержимое по горизонтали: VBox сам по себе растягивает
				// прямых детей на всю ширину.
				Layout: declarative.HBox{MarginsZero: true},
				Children: []declarative.Widget{
					declarative.HSpacer{},
					declarative.Label{
						Text: "Войдите через аккаунт Twitch",
					},
					declarative.HSpacer{},
				},
			},
			declarative.Composite{
				Layout: declarative.HBox{MarginsZero: true},
				Children: []declarative.Widget{
					declarative.HSpacer{},
					declarative.PushButton{
						AssignTo:  &p.signInBtn,
						Text:      "Войти",
						OnClicked: p.onSignInClicked,
					},
					declarative.HSpacer{},
				},
			},
			deviceCodeLine(&p.deviceCodeLine1),
			deviceCodeLine(&p.deviceCodeLine2),
			deviceCodeLine(&p.deviceCodeLine3),
			declarative.VSpacer{},
		},
	}).Create(declarative.NewBuilder(parent))
}

// resetDeviceCode прячет код с прошлой попытки (если она была) —
// вызывается и перед новой попыткой, и при ошибке.
func (p *signInPage) resetDeviceCode() {
	p.deviceCodeLine1.SetText(blankDeviceCodeText)
	p.deviceCodeLine2.SetText(blankDeviceCodeText)
	p.deviceCodeLine3.SetText(blankDeviceCodeText)
}

func (p *signInPage) onSignInClicked() {
	log.Println("нажата кнопка \"Войти\"")
	p.signInBtn.SetEnabled(false)
	p.resetDeviceCode()
	go p.interactiveSignIn()
}

func (p *signInPage) interactiveSignIn() {
	// -H windowsgui — без консоли, некуда деть стандартный вывод паники
	// Go при крахе горутины; без recover тут любая паника молча
	// убивает весь процесс, не оставляя вообще никакого следа. Лучше
	// потерять эту попытку входа, залогировать и дать пользователю
	// нажать "Войти" ещё раз, чем без объяснений закрыть всё окно.
	defer func() {
		if r := recover(); r != nil {
			log.Println("вход: паника:", r)
			p.root.Synchronize(func() {
				p.resetDeviceCode()
				walk.MsgBox(p.root.Form(), "Не удалось войти", fmt.Sprint(r), walk.MsgBoxIconError)
				p.signInBtn.SetEnabled(true)
			})
		}
	}()

	log.Println("вход: запрашиваю device code")
	result, err := p.signIn(func(userCode, verificationURI string) {
		log.Println("вход: получен device code, показываю пользователю")
		p.root.Synchronize(func() {
			p.deviceCodeLine1.SetText("Откройте")
			p.deviceCodeLine2.SetText(verificationURI)
			p.deviceCodeLine3.SetText("и введите код: " + userCode)
			// SetText не гарантирует немедленную перерисовку сама по
			// себе: updateParentLayout внутри Label.SetText (см. vendor
			// lxn/walk, label.go) пропускает перепозиционирование, если
			// вычисленные Bounds не изменились — а у нас они специально
			// не меняются (см. deviceCodeLineSize выше). Invalidate
			// явный и не зависит от угадывания, всегда ли сама
			// перерисовывается STATIC на WM_SETTEXT в конкретной сборке
			// Windows.
			p.deviceCodeLine1.Invalidate()
			p.deviceCodeLine2.Invalidate()
			p.deviceCodeLine3.Invalidate()
		})
	})

	p.root.Synchronize(func() {
		log.Println("вход: signIn вернулся, err =", err)
		if err != nil {
			log.Println("вход:", err)
			p.resetDeviceCode()
			walk.MsgBox(p.root.Form(), "Не удалось войти", err.Error(), walk.MsgBoxIconError)
			p.signInBtn.SetEnabled(true)
			return
		}

		p.onSuccess(result)
	})
}
