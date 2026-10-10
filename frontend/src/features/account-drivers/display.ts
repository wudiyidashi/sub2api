import { CODEX_HTTP_DRIVER_ID, NATIVE_DRIVER_ID } from './useAccountDrivers'

export function accountDriverLabel(
  driver: { id: string; name?: string } | undefined,
  translate: (key: string) => string
): string {
  if (!driver || driver.id === NATIVE_DRIVER_ID) return translate('admin.accounts.accountDriver.native')
  if (driver.id === CODEX_HTTP_DRIVER_ID) return translate('admin.accounts.accountDriver.enhanced')
  return driver.name || driver.id
}
