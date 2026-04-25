package orchestrator

import (
	"fmt"

	"github.com/nishimoto265/auto-improve/internal/contracts/stepio"
)

func defaultContractDecoders() ContractDecoders {
	return ContractDecoders{
		Step10: func(data []byte, req any) (any, error) {
			request, ok := req.(stepio.Step10Request)
			if !ok {
				return nil, fmt.Errorf("orchestrator: step10 decoder expects Step10Request, got %T", req)
			}
			return stepio.DecodeAndValidateStep10Response(data, request)
		},
		Step20: func(data []byte, req any) (any, error) {
			request, ok := req.(stepio.Step20Request)
			if !ok {
				return nil, fmt.Errorf("orchestrator: step20 decoder expects Step20Request, got %T", req)
			}
			return stepio.DecodeAndValidateStep20Response(data, request)
		},
		Step30: func(data []byte, req any) (any, error) {
			request, ok := req.(stepio.Step30Request)
			if !ok {
				return nil, fmt.Errorf("orchestrator: step30 decoder expects Step30Request, got %T", req)
			}
			return stepio.DecodeAndValidateStep30Response(data, request)
		},
		Step40: func(data []byte, req any) (any, error) {
			request, ok := req.(stepio.Step40Request)
			if !ok {
				return nil, fmt.Errorf("orchestrator: step40 decoder expects Step40Request, got %T", req)
			}
			return stepio.DecodeAndValidateStep40Response(data, request)
		},
		Step50: func(data []byte, req any) (any, error) {
			request, ok := req.(stepio.Step50Request)
			if !ok {
				return nil, fmt.Errorf("orchestrator: step50 decoder expects Step50Request, got %T", req)
			}
			return stepio.DecodeAndValidateStep50Response(data, request)
		},
		Step60: func(data []byte, req any) (any, error) {
			request, ok := req.(stepio.Step60Request)
			if !ok {
				return nil, fmt.Errorf("orchestrator: step60 decoder expects Step60Request, got %T", req)
			}
			return stepio.DecodeAndValidateStep60Response(data, request)
		},
		Step70: func(data []byte, req any) (any, error) {
			request, ok := req.(stepio.Step70Request)
			if !ok {
				return nil, fmt.Errorf("orchestrator: step70 decoder expects Step70Request, got %T", req)
			}
			return stepio.DecodeAndValidateStep70Response(data, request)
		},
	}
}
