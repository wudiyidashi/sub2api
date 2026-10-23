// 只替换基础控件，额度投影和业务组件使用实际固定源码。
export const defineTableColumns = (columns: unknown) => columns
export const ZButton = { template: '<button><slot /></button>' }
export const ZIconButton = ZButton
export const ZMenuItem = ZButton
export const ZPopover = { template: '<div><slot name="reference" :open="false" /><slot :close="() => {}" /></div>' }
