// Tree-shaken ECharts bundle: only the chart types and components the
// dashboard uses (line/area trend, bars, pie/donut + tooltip/grid).
// Keeps the production JS small and auditable; no CDN.
import * as echarts from 'echarts/core';
import { LineChart, BarChart, PieChart } from 'echarts/charts';
import {
	GridComponent,
	TooltipComponent,
	LegendComponent
} from 'echarts/components';
import { CanvasRenderer } from 'echarts/renderers';

echarts.use([LineChart, BarChart, PieChart, GridComponent, TooltipComponent, LegendComponent, CanvasRenderer]);

export default echarts;
export type ECharts = ReturnType<typeof echarts.init>;
