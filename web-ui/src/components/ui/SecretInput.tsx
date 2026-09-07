import { useState, type InputHTMLAttributes } from "react";
import { Eye, EyeOff } from "lucide-react";

import { cn } from "../../lib/cn";
import { PASSWORD_MANAGER_OFF, SECRET_MASK_CLASS, secretMaskSupported } from "../../lib/secretMask";
import { Input } from "./form";

type SecretInputProps = Omit<InputHTMLAttributes<HTMLInputElement>, "type"> & {
  mono?: boolean;
  // ownPassword — секрет принадлежит САМОМУ пользователю (форма сброса своего
  // пароля). Единственный случай, когда менеджер паролей уместен: ему и надо
  // предложить сгенерировать пароль и сохранить его. Во всех остальных местах
  // приложения SecretInput держит секрет ЧУЖОЙ системы, и менеджеру там делать
  // нечего — поэтому умолчание обратное, см. §98.7.
  ownPassword?: boolean;
};

// SecretInput — поле для чувствительных данных авторизации (§28, Пункт 3).
// По умолчанию значение скрыто, кнопка-глазик раскрывает введённое в форме
// значение. Сохранённые секреты бэкенд наружу не отдаёт (приходят только флаги
// *_set), поэтому раскрывается только то, что пользователь сейчас вводит.
//
// §98.7: поле секрета ЧУЖОЙ системы (узел, RabbitMQ, ClickHouse, SMTP, Sentry)
// не должно опознаваться менеджерами паролей. Одного `autocomplete` мало —
// ровно как в §90.3 для полей поиска, здесь собрано несколько приёмов, и каждый
// закрывает своего автозаполнителя:
//
//   - маскировка CSS на ОБЫЧНОМ текстовом поле вместо `type="password"` —
//     встроенный менеджер Chrome/Edge ищет поле пароля по типу, и текстовое
//     поле он не предлагает ни заполнить, ни сохранить. Где
//     `-webkit-text-security` не поддержан, возвращаемся к `type="password"`:
//     это сегодняшнее поведение, а показывать секрет открытым текстом нельзя;
//   - `autocomplete="off"` вместо `new-password` — само слово `password` в
//     значении атрибута служит эвристике подсказкой;
//   - нейтральное `name="secret"` — эвристика Chrome смотрит и на имя поля
//     (тот же приём, что `name="q"` у SearchInput);
//   - `data-1p-ignore` / `data-lpignore` / `data-form-type` — сторонние
//     менеджеры (1Password, LastPass, Dashlane), у которых своя эвристика.
//
// Атрибуты стоят ДО {...rest}: вызывающая сторона при необходимости
// переопределяет любой из них.
export function SecretInput({ className, mono, ownPassword, ...rest }: SecretInputProps) {
  const [show, setShow] = useState(false);

  // Собственный пароль пользователя маскируется как раньше — типом поля, иначе
  // менеджер не предложит ни сгенерировать, ни сохранить его.
  const cssMasked = !ownPassword && !show && secretMaskSupported();
  const type = cssMasked || show ? "text" : "password";

  const managerHints = ownPassword
    ? { autoComplete: "new-password" }
    : { ...PASSWORD_MANAGER_OFF, name: "secret" };

  return (
    <div className="relative">
      <Input
        {...managerHints}
        {...rest}
        type={type}
        mono={mono}
        className={cn("pr-9", cssMasked && SECRET_MASK_CLASS, className)}
      />
      <button
        type="button"
        onClick={() => setShow((s) => !s)}
        className="absolute right-2 top-1/2 -translate-y-1/2 text-fg-subtle hover:text-fg"
        tabIndex={-1}
        aria-label={show ? "hide secret" : "show secret"}
      >
        {show ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
      </button>
    </div>
  );
}
