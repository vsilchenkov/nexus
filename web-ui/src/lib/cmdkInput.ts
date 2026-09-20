import { useCallback, type MutableRefObject, type Ref } from "react";

/**
 * useCmdkInputType — ref-колбэк, выставляющий полю поиска внутри cmdk-попапа
 * тип (§90.3).
 *
 * Пропсом тип не передать: cmdk дописывает свой `type="text"` ПОСЛЕ спреда
 * наших пропсов и затирает переданный. Поэтому тип ставится императивно, при
 * монтировании; React после этого к атрибуту не возвращается (в его модели тип
 * не менялся), поэтому присвоение держится. Внешний ref вызывающей стороны
 * колбэк прокидывает дальше — иначе поле потеряло бы фокусировку по ref.
 */
export function useCmdkInputType(type: string, ref?: Ref<HTMLInputElement>) {
  return useCallback(
    (node: HTMLInputElement | null) => {
      if (node) node.type = type;
      if (typeof ref === "function") ref(node);
      else if (ref) (ref as MutableRefObject<HTMLInputElement | null>).current = node;
    },
    [ref, type],
  );
}
