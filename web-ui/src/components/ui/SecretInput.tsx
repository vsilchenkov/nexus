import { useState, type InputHTMLAttributes } from "react";
import { Eye, EyeOff } from "lucide-react";

import { cn } from "../../lib/cn";
import { Input } from "./form";

type SecretInputProps = Omit<InputHTMLAttributes<HTMLInputElement>, "type"> & {
  mono?: boolean;
};

// SecretInput — поле для чувствительных данных авторизации (§28, Пункт 3).
// По умолчанию значение скрыто (type=password, звёздочки), кнопка-глазик
// раскрывает введённое в форме значение. Сохранённые секреты бэкенд наружу не
// отдаёт (приходят только флаги *_set), поэтому раскрывается только то, что
// пользователь сейчас вводит.
export function SecretInput({ className, mono, ...rest }: SecretInputProps) {
  const [show, setShow] = useState(false);
  return (
    <div className="relative">
      <Input
        {...rest}
        type={show ? "text" : "password"}
        mono={mono}
        className={cn("pr-9", className)}
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
