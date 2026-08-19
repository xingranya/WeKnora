export type TranslateParserEngineName = (key: string) => string

/** 使用产品翻译展示解析器名称，未登记的第三方解析器保留其原始名称。 */
export function parserEngineDisplayName(
  name: string,
  translate: TranslateParserEngineName,
): string {
  const key = `kbSettings.parser.engines.${name}.name`
  const translated = translate(key)
  return translated !== key ? translated : name
}
