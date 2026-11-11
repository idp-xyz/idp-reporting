// ESLint for the console's browser scripts and the Node scripts and tests that drive it. The rules are the ones that find bugs (an
// undefined name, a variable that is never read, an unreachable branch, a duplicated key), not style: style is Prettier's
// (.prettierrc.json, `npm run format`; ADR 0014 decision 9), and CI's console job checks both on every change.
// package.json overrides minimatch to 11.0.5 because eslint 10.14 still requires minimatch 10, which advisory GHSA-g3hv-75wg-m249
// covers. ESLint never sets the optimizationLevel that reaches the flaw, but `npm audit` cannot tell; drop the override once eslint's
// own range admits 11.0.1 or later.
import js from '@eslint/js';
import globals from 'globals';

// The console's scripts are classic scripts loaded in order by index.html and embed.html: each defines a window.create* factory that
// app.js (and embed.js) call. They share no module scope, so these names are the contract between them.
const factories = {
  createReportingDashboard: 'readonly',
  createReportingCanvas: 'readonly',
  createReportingVersions: 'readonly',
  createReportingStudio: 'readonly',
  createReportingLibrary: 'readonly',
  createReportingDocuments: 'readonly',
  createReportingBoardStudio: 'readonly',
  createReportingEmbed: 'readonly',
  createReportingIssuePage: 'readonly',
  createReportingCompanyPicker: 'readonly',
};

export default [
  {ignores: ['node_modules/**', 'evidence/**', 'docs/**', 'spikes/**', '**/*.min.js']},
  js.configs.recommended,
  {
    files: ['console/assets/*.js', 'console/embed-host/*.js'],
    languageOptions: {ecmaVersion: 2024, sourceType: 'script', globals: {...globals.browser, ...factories}},
    rules: {
      // The scripts are wrapped in a function or attach to window; a top-level declaration is the factory itself.
      'no-unused-vars': ['error', {vars: 'local', args: 'none', caughtErrors: 'none'}],
      'no-empty': ['error', {allowEmptyCatch: true}],
      // ADR 0008: the console builds nodes with the DOM API and never writes markup from strings; these would bypass it.
      'no-restricted-properties': [
        'error',
        {property: 'innerHTML', message: 'Build nodes with the DOM API; the console never writes markup from strings.'},
        {property: 'outerHTML', message: 'Build nodes with the DOM API.'},
        {property: 'insertAdjacentHTML', message: 'Build nodes with the DOM API.'},
      ],
      'no-eval': 'error',
      'no-implied-eval': 'error',
      'no-new-func': 'error',
      // Input checks reject control characters on purpose (names, labels, field ids): /[\x00-\x1f\x7f]/ is the rule, not a typo.
      'no-control-regex': 'off',
      // A parse or validation failure becomes the user-facing message on purpose; the original error is not shown to the person.
      'preserve-caught-error': 'off',
    },
  },
  {
    files: ['console/*.mjs', 'scripts/*.mjs', 'eslint.config.mjs'],
    languageOptions: {ecmaVersion: 2024, sourceType: 'module', globals: {...globals.node}},
    rules: {
      'no-unused-vars': ['error', {args: 'none', caughtErrors: 'none'}],
      'no-empty': ['error', {allowEmptyCatch: true}],
      // A test may fail with its own message for a caught error.
      'preserve-caught-error': 'off',
    },
  },
];
