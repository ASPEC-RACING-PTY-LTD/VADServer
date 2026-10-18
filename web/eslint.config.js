// ESLint flat config.
//
// typescript-eslint 8.x only supports TypeScript < 6.1, while this project
// builds with TypeScript 7 (native compiler, no JS compiler API). The
// typescript-eslint packages are therefore pointed at a TypeScript 6 copy
// installed under the `typescript6` alias; `tsc` itself still uses TS 7.
// Remove this hook once typescript-eslint supports TypeScript 7.
import { registerHooks } from 'node:module'

registerHooks({
  resolve(specifier, context, next) {
    if (
      specifier === 'typescript' &&
      context.parentURL &&
      /[\/]node_modules[\/](@typescript-eslint|typescript-eslint|ts-api-utils)[\/]/.test(
        decodeURIComponent(context.parentURL),
      )
    ) {
      return next('typescript6', context)
    }
    return next(specifier, context)
  },
})

const { default: js } = await import('@eslint/js')
const { default: globals } = await import('globals')
const { default: reactHooks } = await import('eslint-plugin-react-hooks')
const { default: tseslint } = await import('typescript-eslint')

export default tseslint.config(
  { ignores: ['dist', 'node_modules', 'playwright-report', 'test-results'] },
  {
    files: ['**/*.{ts,tsx}'],
    extends: [js.configs.recommended, ...tseslint.configs.recommended],
    languageOptions: {
      ecmaVersion: 2022,
      globals: { ...globals.browser },
    },
    plugins: { 'react-hooks': reactHooks },
    rules: {
      'react-hooks/rules-of-hooks': 'error',
      'react-hooks/exhaustive-deps': 'warn',
      '@typescript-eslint/no-unused-vars': ['warn', { argsIgnorePattern: '^_', varsIgnorePattern: '^_' }],
      '@typescript-eslint/no-explicit-any': 'warn',
      '@typescript-eslint/no-unused-expressions': ['error', { allowShortCircuit: true, allowTernary: true }],
    },
  },
  {
    files: ['*.config.{js,ts}', 'e2e/**/*.ts'],
    languageOptions: { globals: { ...globals.node } },
  },
)
