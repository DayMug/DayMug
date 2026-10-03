/// <reference types="vite/client" />

declare module "*.vue" {
  import type { DefineComponent } from "vue";
  const component: DefineComponent<object, object, unknown>;
  export default component;
}

// The exceljs package root pulls in fs/stream and cannot run in a browser;
// `dist/exceljs.min.js` is the browser bundle but is not covered by the
// package's type entry. Its shape is the package's own, so borrow those types
// rather than falling back to `any`.
declare module "exceljs/dist/exceljs.min.js" {
  import type ExcelJS from "exceljs";
  const bundle: typeof ExcelJS;
  export default bundle;
}
